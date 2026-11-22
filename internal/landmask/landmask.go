package landmask

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/raster"
)

const (
	shapefileCode      = 9994
	shapefileVersion   = 1000
	shapeNull          = 0
	shapePolygon       = 5
	shapePolygonZ      = 15
	shapePolygonM      = 25
	shapefileHeaderLen = 100
	maxDegStep         = 0.02
)

// BBox represents a geographic bounding box in WGS84 degrees (EPSG:4326).
type BBox struct {
	MinLon float64
	MinLat float64
	MaxLon float64
	MaxLat float64
}

/*
Intersects reports whether b overlaps the supplied geographic bounding box.
*/
func (b BBox) Intersects(minLon, minLat, maxLon, maxLat float64) bool {
	return maxLon >= b.MinLon && minLon <= b.MaxLon && maxLat >= b.MinLat && minLat <= b.MaxLat
}

// Stats summarizes how many land polygon records were inspected and rasterized.
type Stats struct {
	TotalRecords    int
	MatchedRecords  int
	RasterizedRings int
}

/*
QueryBBoxFromLAEA converts a planar LAEA bounding box into an enclosing WGS84 degree bounding box.
It samples sixteen points along each of the four planar edges to account for curved parallels and meridians,
then applies a small 0.1 degree margin so split tiles intersecting curved boundaries are never skipped.
*/
func QueryBBoxFromLAEA(minX, minY, maxX, maxY float64, proj geo.LAEA) BBox {
	const samples = 16
	const padDeg = 0.1

	minLat, minLon := math.Inf(1), math.Inf(1)
	maxLat, maxLon := math.Inf(-1), math.Inf(-1)
	wrapsAntimeridian := false

	sampleEdge := func(x0, y0, x1, y1 float64) {
		var prevLon float64
		for i := 0; i <= samples; i++ {
			t := float64(i) / float64(samples)
			x := x0 + t*(x1-x0)
			y := y0 + t*(y1-y0)
			lat, lon := proj.Inverse(x, y)
			if i > 0 && math.Abs(lon-prevLon) > 180.0 {
				wrapsAntimeridian = true
			}
			prevLon = lon
			if lat < minLat {
				minLat = lat
			}
			if lat > maxLat {
				maxLat = lat
			}
			if lon < minLon {
				minLon = lon
			}
			if lon > maxLon {
				maxLon = lon
			}
		}
	}

	sampleEdge(minX, minY, maxX, minY)
	sampleEdge(maxX, minY, maxX, maxY)
	sampleEdge(maxX, maxY, minX, maxY)
	sampleEdge(minX, maxY, minX, minY)

	// Poles can lie strictly inside the planar box and thus be missed by edge sampling.
	checkPole := func(poleLat float64) {
		px, py := proj.Forward(poleLat, 0.0)
		if px < minX || px > maxX || py < minY || py > maxY {
			return
		}
		invLat, _ := proj.Inverse(px, py)
		if math.Abs(invLat-poleLat) > 1e-3 {
			return
		}
		if poleLat > 0 {
			maxLat = 90.0
		} else {
			minLat = -90.0
		}
		wrapsAntimeridian = true
	}
	checkPole(90.0)
	checkPole(-90.0)

	if wrapsAntimeridian {
		minLon = -180.0
		maxLon = 180.0
	} else {
		minLon = max(-180.0, minLon-padDeg)
		maxLon = min(180.0, maxLon+padDeg)
	}
	minLat = max(-90.0, minLat-padDeg)
	maxLat = min(90.0, maxLat+padDeg)

	return BBox{
		MinLon: minLon,
		MinLat: minLat,
		MaxLon: maxLon,
		MaxLat: maxLat,
	}
}

/*
Validate verifies that path points to a readable WGS84 land-polygon source (.shp, .zip containing .shp, directory containing .shp, or .geojson/.json).
For ESRI Shapefiles and ZIP archives, the 100-byte shapefile header is validated immediately so wrong projections such as EPSG:3857 fail before Pass 1 begins.
*/
func Validate(path string) error {
	resolved, err := resolveSourcePath(path)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".shp":
		f, err := os.Open(resolved)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = readShapefileHeader(f)
		return err
	case ".zip":
		zr, err := zip.OpenReader(resolved)
		if err != nil {
			return fmt.Errorf("open zip %q: %w", resolved, err)
		}
		defer func() { _ = zr.Close() }()
		zf, err := findShapefileInZip(zr.File)
		if err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		_, err = readShapefileHeader(rc)
		return err
	case ".geojson", ".json":
		fi, err := os.Stat(resolved)
		if err != nil {
			return err
		}
		if fi.Size() == 0 {
			return fmt.Errorf("empty GeoJSON file %q", resolved)
		}
		return nil
	default:
		return fmt.Errorf("unsupported land polygons format %q (expected .shp, .zip, .geojson, or .json)", resolved)
	}
}

/*
Rasterize streams land polygons intersecting queryBBox from path, projects them into LAEA meter coordinates, and fills them into landBS.
Supported inputs are ESRI Shapefiles (.shp, using fast seeking to skip non-intersecting tiles), ZIP archives containing a .shp file (such as land-polygons-split-4326.zip),
directories containing a single .shp file, and RFC 7946 WGS84 GeoJSON files (.geojson/.json).
*/
func Rasterize(
	path string,
	queryBBox BBox,
	proj geo.LAEA,
	landBS *raster.Bitset,
	originX, originY, invRes float64,
) (Stats, error) {
	resolved, err := resolveSourcePath(path)
	if err != nil {
		return Stats{}, err
	}
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".shp":
		f, err := os.Open(resolved)
		if err != nil {
			return Stats{}, err
		}
		defer func() { _ = f.Close() }()
		return rasterizeShapefile(f, f, queryBBox, proj, landBS, originX, originY, invRes)
	case ".zip":
		zr, err := zip.OpenReader(resolved)
		if err != nil {
			return Stats{}, fmt.Errorf("open zip %q: %w", resolved, err)
		}
		defer func() { _ = zr.Close() }()
		zf, err := findShapefileInZip(zr.File)
		if err != nil {
			return Stats{}, err
		}
		rc, err := zf.Open()
		if err != nil {
			return Stats{}, err
		}
		defer func() { _ = rc.Close() }()
		return rasterizeShapefile(rc, nil, queryBBox, proj, landBS, originX, originY, invRes)
	case ".geojson", ".json":
		return rasterizeGeoJSON(resolved, queryBBox, proj, landBS, originX, originY, invRes)
	default:
		return Stats{}, fmt.Errorf("unsupported land polygons format %q (expected .shp, .zip, .geojson, or .json)", resolved)
	}
}

/*
resolveSourcePath resolves a file or directory path to a concrete land-polygon file path.
When path is a directory, it prefers land_polygons.shp or falls back to the unique .shp file inside that directory.
*/
func resolveSourcePath(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return path, nil
	}

	preferred := filepath.Join(path, "land_polygons.shp")
	if pfi, perr := os.Stat(preferred); perr == nil && !pfi.IsDir() {
		return preferred, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var shpFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if strings.EqualFold(filepath.Ext(name), ".shp") {
			shpFiles = append(shpFiles, filepath.Join(path, name))
		}
	}
	if len(shpFiles) == 1 {
		return shpFiles[0], nil
	}
	if len(shpFiles) == 0 {
		return "", fmt.Errorf("no .shp file found in directory %q", path)
	}
	return "", fmt.Errorf("multiple .shp files found in directory %q; specify the .shp file explicitly", path)
}

/*
findShapefileInZip locates the primary ESRI Shapefile entry inside a ZIP archive while ignoring macOS resource metadata.
*/
func findShapefileInZip(files []*zip.File) (*zip.File, error) {
	var first *zip.File
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		base := filepath.Base(name)
		if strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(base, "._") {
			continue
		}
		if !strings.EqualFold(filepath.Ext(base), ".shp") {
			continue
		}
		if strings.EqualFold(base, "land_polygons.shp") {
			return f, nil
		}
		if first == nil {
			first = f
		}
	}
	if first == nil {
		return nil, fmt.Errorf("no .shp entry found in ZIP archive")
	}
	return first, nil
}

type shpHeader struct {
	fileLenBytes int64
	shapeType    uint32
	bbox         BBox
}

/*
readShapefileHeader decodes and validates the 100-byte ESRI Shapefile header.
It rejects non-polygon shapefiles and coordinate extents outside WGS84 degrees (such as EPSG:3857 Web Mercator).
*/
func readShapefileHeader(r io.Reader) (shpHeader, error) {
	var hdr [shapefileHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return shpHeader{}, fmt.Errorf("read shapefile header: %w", err)
	}
	fileCode := binary.BigEndian.Uint32(hdr[0:4])
	if fileCode != shapefileCode {
		return shpHeader{}, fmt.Errorf("invalid shapefile magic code %d (expected %d)", fileCode, shapefileCode)
	}
	fileLenWords := binary.BigEndian.Uint32(hdr[24:28])
	version := binary.LittleEndian.Uint32(hdr[28:32])
	if version != shapefileVersion {
		return shpHeader{}, fmt.Errorf("unsupported shapefile version %d (expected %d)", version, shapefileVersion)
	}
	shapeType := binary.LittleEndian.Uint32(hdr[32:36])
	switch shapeType {
	case shapeNull, shapePolygon, shapePolygonZ, shapePolygonM:
	default:
		return shpHeader{}, fmt.Errorf("unsupported shapefile geometry type %d (expected Polygon=5, PolygonZ=15, or PolygonM=25)", shapeType)
	}

	minX := math.Float64frombits(binary.LittleEndian.Uint64(hdr[36:44]))
	minY := math.Float64frombits(binary.LittleEndian.Uint64(hdr[44:52]))
	maxX := math.Float64frombits(binary.LittleEndian.Uint64(hdr[52:60]))
	maxY := math.Float64frombits(binary.LittleEndian.Uint64(hdr[60:68]))

	fileLenBytes := int64(fileLenWords) * 2
	if fileLenBytes > shapefileHeaderLen {
		if err := validateWGS84Bounds(minX, minY, maxX, maxY); err != nil {
			return shpHeader{}, err
		}
	}

	return shpHeader{
		fileLenBytes: fileLenBytes,
		shapeType:    shapeType,
		bbox: BBox{
			MinLon: minX,
			MinLat: minY,
			MaxLon: maxX,
			MaxLat: maxY,
		},
	}, nil
}

/*
validateWGS84Bounds verifies that bounding-box coordinates lie within valid WGS84 degree limits.
This catches accidental use of Web Mercator (EPSG:3857) shapefiles immediately with an actionable error message.
*/
func validateWGS84Bounds(minLon, minLat, maxLon, maxLat float64) error {
	if math.IsNaN(minLon) || math.IsNaN(minLat) || math.IsNaN(maxLon) || math.IsNaN(maxLat) ||
		minLon < -181.0 || maxLon > 181.0 || minLat < -91.0 || maxLat > 91.0 {
		return fmt.Errorf(
			"coordinates [%.2f, %.2f, %.2f, %.2f] exceed WGS84 degree bounds (EPSG:4326); download land-polygons-split-4326 (not 3857)",
			minLon, minLat, maxLon, maxLat,
		)
	}
	return nil
}

/*
discardBytes advances a buffered reader by exactly n bytes using bounded chunk discards.
*/
func discardBytes(br *bufio.Reader, n int64) error {
	for n > 0 {
		chunk := min(n, 1<<20)
		d, err := br.Discard(int(chunk))
		n -= int64(d)
		if err != nil {
			return err
		}
	}
	return nil
}

/*
rasterizeShapefile streams polygon records from an ESRI Shapefile reader, skipping non-intersecting records via BBox checks and seeking when available.
*/
func rasterizeShapefile(
	r io.Reader,
	seeker io.Seeker,
	queryBBox BBox,
	proj geo.LAEA,
	landBS *raster.Bitset,
	originX, originY, invRes float64,
) (Stats, error) {
	br := bufio.NewReaderSize(r, 256*1024)
	hdr, err := readShapefileHeader(br)
	if err != nil {
		return Stats{}, err
	}
	if hdr.fileLenBytes <= shapefileHeaderLen {
		return Stats{}, nil
	}
	if hdr.bbox.MaxLon >= hdr.bbox.MinLon && hdr.bbox.MaxLat >= hdr.bbox.MinLat &&
		!queryBBox.Intersects(hdr.bbox.MinLon, hdr.bbox.MinLat, hdr.bbox.MaxLon, hdr.bbox.MaxLat) {
		return Stats{}, nil
	}

	var stats Stats
	var xsBuf []float64
	var rawBuf []byte
	var partsBuf []int
	var lonLatBuf [][2]float64
	var recHdr [8]byte
	var stBuf [4]byte
	var bboxBuf [32]byte
	var countsBuf [8]byte

	pos := int64(shapefileHeaderLen)
	advance := func(skip int64) error {
		if skip < 0 {
			return fmt.Errorf("corrupt shapefile record: negative skip %d", skip)
		}
		if skip == 0 {
			return nil
		}
		pos += skip
		if skip <= int64(br.Buffered()) {
			return discardBytes(br, skip)
		}
		if seeker != nil {
			if _, serr := seeker.Seek(pos, io.SeekStart); serr != nil {
				return serr
			}
			br.Reset(r)
			return nil
		}

		return discardBytes(br, skip)
	}

	for {
		_, rerr := io.ReadFull(br, recHdr[:])
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return stats, fmt.Errorf("read shapefile record header: %w", rerr)
		}
		contentWords := binary.BigEndian.Uint32(recHdr[4:8])
		if contentWords < 2 {
			return stats, fmt.Errorf("invalid shapefile record length: %d words", contentWords)
		}
		contentBytes := int64(contentWords) * 2
		pos += 8
		recordEnd := pos + contentBytes

		if _, rerr := io.ReadFull(br, stBuf[:]); rerr != nil {
			return stats, fmt.Errorf("read shapefile record type: %w", rerr)
		}
		pos += 4
		recType := binary.LittleEndian.Uint32(stBuf[:])
		if recType == shapeNull {
			if aerr := advance(recordEnd - pos); aerr != nil {
				return stats, aerr
			}
			continue
		}
		switch recType {
		case shapePolygon, shapePolygonZ, shapePolygonM:
		default:
			return stats, fmt.Errorf("unsupported shapefile record geometry type %d", recType)
		}

		stats.TotalRecords++
		if contentBytes < 44 {
			return stats, fmt.Errorf("truncated shapefile polygon record (%d bytes)", contentBytes)
		}

		if _, rerr := io.ReadFull(br, bboxBuf[:]); rerr != nil {
			return stats, fmt.Errorf("read shapefile record bbox: %w", rerr)
		}
		pos += 32
		rMinLon := math.Float64frombits(binary.LittleEndian.Uint64(bboxBuf[0:8]))
		rMinLat := math.Float64frombits(binary.LittleEndian.Uint64(bboxBuf[8:16]))
		rMaxLon := math.Float64frombits(binary.LittleEndian.Uint64(bboxBuf[16:24]))
		rMaxLat := math.Float64frombits(binary.LittleEndian.Uint64(bboxBuf[24:32]))

		if !queryBBox.Intersects(rMinLon, rMinLat, rMaxLon, rMaxLat) {
			if aerr := advance(recordEnd - pos); aerr != nil {
				return stats, aerr
			}
			continue
		}

		if _, rerr := io.ReadFull(br, countsBuf[:]); rerr != nil {
			return stats, fmt.Errorf("read shapefile polygon counts: %w", rerr)
		}
		pos += 8
		numPartsU := binary.LittleEndian.Uint32(countsBuf[0:4])
		numPointsU := binary.LittleEndian.Uint32(countsBuf[4:8])
		if numPartsU > math.MaxInt32 || numPointsU > math.MaxInt32 {
			return stats, fmt.Errorf("shapefile polygon counts out of bounds: parts=%d points=%d", numPartsU, numPointsU)
		}
		numParts := int(numPartsU)
		numPoints := int(numPointsU)
		payloadBytes := int64(numParts)*4 + int64(numPoints)*16
		if int64(44)+payloadBytes > contentBytes {
			return stats, fmt.Errorf("truncated shapefile polygon payload: need %d bytes, record has %d", 44+payloadBytes, contentBytes)
		}
		if numParts == 0 || numPoints < 3 {
			if aerr := advance(recordEnd - pos); aerr != nil {
				return stats, aerr
			}
			continue
		}

		needLen := int(payloadBytes)
		if cap(rawBuf) < needLen {
			rawBuf = make([]byte, needLen)
		} else {
			rawBuf = rawBuf[:needLen]
		}
		if _, rerr := io.ReadFull(br, rawBuf); rerr != nil {
			return stats, fmt.Errorf("read shapefile polygon geometry: %w", rerr)
		}
		pos += payloadBytes

		if aerr := advance(recordEnd - pos); aerr != nil {
			return stats, aerr
		}

		partsBuf = partsBuf[:0]
		for i := 0; i < numParts; i++ {
			idxU := binary.LittleEndian.Uint32(rawBuf[i*4 : (i+1)*4])
			if idxU > numPointsU {
				return stats, fmt.Errorf("invalid shapefile part start index %d (numPoints=%d)", idxU, numPoints)
			}
			idx := int(idxU)
			if i > 0 && idx < partsBuf[i-1] {
				return stats, fmt.Errorf("non-monotonic shapefile part start index %d < %d", idx, partsBuf[i-1])
			}
			partsBuf = append(partsBuf, idx)
		}

		ptsOff := numParts * 4
		rings := make([][]geo.Pt64, 0, numParts)
		for pi := 0; pi < numParts; pi++ {
			start := partsBuf[pi]
			end := numPoints
			if pi+1 < numParts {
				end = partsBuf[pi+1]
			}
			if end-start < 3 {
				continue
			}
			lonLatBuf = lonLatBuf[:0]
			for k := start; k < end; k++ {
				off := ptsOff + k*16
				lon := math.Float64frombits(binary.LittleEndian.Uint64(rawBuf[off : off+8]))
				lat := math.Float64frombits(binary.LittleEndian.Uint64(rawBuf[off+8 : off+16]))
				lonLatBuf = append(lonLatBuf, [2]float64{lon, lat})
			}
			if pr := projectRing(lonLatBuf, proj); len(pr) >= 4 {
				rings = append(rings, pr)
			}
		}

		if len(rings) > 0 {
			stats.MatchedRecords++
			stats.RasterizedRings += len(rings)
			xsBuf = raster.RasterizeRings(landBS, rings, originX, originY, invRes, true, xsBuf)
		}
	}

	return stats, nil
}

/*
projectRing projects a WGS84 longitude-latitude ring into closed planar LAEA meter coordinates.
Long straight edges in degrees (such as split shapefile tile borders) are subdivided to at most maxDegStep degrees
so adjacent tiles follow true curved parallels and meridians without projection slivers.
*/
func projectRing(lonLat [][2]float64, proj geo.LAEA) []geo.Pt64 {
	if len(lonLat) < 3 {
		return nil
	}
	segCount := len(lonLat) - 1
	if lonLat[0] != lonLat[len(lonLat)-1] {
		segCount = len(lonLat)
	}
	pts := make([]geo.Pt64, 0, len(lonLat)+1)
	x0, y0 := proj.Forward(lonLat[0][1], lonLat[0][0])
	pts = append(pts, geo.Pt64{X: x0, Y: y0})

	for i := 0; i < segCount; i++ {
		a := lonLat[i]
		b := lonLat[(i+1)%len(lonLat)]
		dLon := b[0] - a[0]
		dLat := b[1] - a[1]
		maxDelta := max(math.Abs(dLon), math.Abs(dLat))
		steps := 1
		if maxDelta > maxDegStep {
			steps = int(math.Ceil(maxDelta / maxDegStep))
		}
		for s := 1; s <= steps; s++ {
			t := float64(s) / float64(steps)
			lon := a[0] + t*dLon
			lat := a[1] + t*dLat
			x, y := proj.Forward(lat, lon)
			pts = append(pts, geo.Pt64{X: x, Y: y})
		}
	}
	pts[len(pts)-1] = pts[0]
	return pts
}

type geoJSONEnvelope struct {
	Type        string            `json:"type"`
	Features    []json.RawMessage `json:"features"`
	Geometry    json.RawMessage   `json:"geometry"`
	Coordinates json.RawMessage   `json:"coordinates"`
}

/*
rasterizeGeoJSON loads Polygon and MultiPolygon geometries from an RFC 7946 WGS84 GeoJSON file and rasterizes intersecting polygons into landBS.
*/
func rasterizeGeoJSON(
	path string,
	queryBBox BBox,
	proj geo.LAEA,
	landBS *raster.Bitset,
	originX, originY, invRes float64,
) (Stats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Stats{}, err
	}

	var stats Stats
	var xsBuf []float64

	handlePolygon := func(rawRings [][][]float64) error {
		stats.TotalRecords++
		minLon, minLat := math.Inf(1), math.Inf(1)
		maxLon, maxLat := math.Inf(-1), math.Inf(-1)
		for _, r := range rawRings {
			for _, pt := range r {
				if len(pt) < 2 {
					continue
				}
				lon, lat := pt[0], pt[1]
				if lon < minLon {
					minLon = lon
				}
				if lon > maxLon {
					maxLon = lon
				}
				if lat < minLat {
					minLat = lat
				}
				if lat > maxLat {
					maxLat = lat
				}
			}
		}
		if math.IsInf(minLon, 1) {
			return nil
		}
		if verr := validateWGS84Bounds(minLon, minLat, maxLon, maxLat); verr != nil {
			return verr
		}
		if !queryBBox.Intersects(minLon, minLat, maxLon, maxLat) {
			return nil
		}

		rings := make([][]geo.Pt64, 0, len(rawRings))
		for _, r := range rawRings {
			if len(r) < 3 {
				continue
			}
			lonLat := make([][2]float64, 0, len(r))
			for _, pt := range r {
				if len(pt) >= 2 {
					lonLat = append(lonLat, [2]float64{pt[0], pt[1]})
				}
			}
			if pr := projectRing(lonLat, proj); len(pr) >= 4 {
				rings = append(rings, pr)
			}
		}
		if len(rings) > 0 {
			stats.MatchedRecords++
			stats.RasterizedRings += len(rings)
			xsBuf = raster.RasterizeRings(landBS, rings, originX, originY, invRes, true, xsBuf)
		}
		return nil
	}

	var handleRaw func(raw json.RawMessage) error
	handleRaw = func(raw json.RawMessage) error {
		if len(raw) == 0 || string(raw) == "null" {
			return nil
		}
		var env geoJSONEnvelope
		if uerr := json.Unmarshal(raw, &env); uerr != nil {
			return fmt.Errorf("parse GeoJSON: %w", uerr)
		}
		switch env.Type {
		case "FeatureCollection":
			for _, feat := range env.Features {
				if ferr := handleRaw(feat); ferr != nil {
					return ferr
				}
			}
		case "Feature":
			return handleRaw(env.Geometry)
		case "Polygon":
			var poly [][][]float64
			if uerr := json.Unmarshal(env.Coordinates, &poly); uerr != nil {
				return fmt.Errorf("parse GeoJSON Polygon: %w", uerr)
			}
			return handlePolygon(poly)
		case "MultiPolygon":
			var multi [][][][]float64
			if uerr := json.Unmarshal(env.Coordinates, &multi); uerr != nil {
				return fmt.Errorf("parse GeoJSON MultiPolygon: %w", uerr)
			}
			for _, poly := range multi {
				if perr := handlePolygon(poly); perr != nil {
					return perr
				}
			}
		}
		return nil
	}

	if err := handleRaw(data); err != nil {
		return stats, err
	}
	return stats, nil
}

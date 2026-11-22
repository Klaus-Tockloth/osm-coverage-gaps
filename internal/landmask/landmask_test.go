package landmask

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/raster"
)

type testShpRecord struct {
	nullRec bool
	rings   [][][2]float64
}

func buildTestShapefile(t *testing.T, recs []testShpRecord) []byte {
	t.Helper()

	var body bytes.Buffer
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)

	for i, rec := range recs {
		var content bytes.Buffer
		if rec.nullRec {
			_ = binary.Write(&content, binary.LittleEndian, uint32(shapeNull))
		} else {
			rMinX, rMinY := math.Inf(1), math.Inf(1)
			rMaxX, rMaxY := math.Inf(-1), math.Inf(-1)
			var parts []uint32
			var points [][2]float64
			for _, ring := range rec.rings {
				parts = append(parts, uint32(len(points)))
				for _, pt := range ring {
					points = append(points, pt)
					if pt[0] < rMinX {
						rMinX = pt[0]
					}
					if pt[0] > rMaxX {
						rMaxX = pt[0]
					}
					if pt[1] < rMinY {
						rMinY = pt[1]
					}
					if pt[1] > rMaxY {
						rMaxY = pt[1]
					}
				}
			}
			if rMinX < minX {
				minX = rMinX
			}
			if rMaxX > maxX {
				maxX = rMaxX
			}
			if rMinY < minY {
				minY = rMinY
			}
			if rMaxY > maxY {
				maxY = rMaxY
			}

			_ = binary.Write(&content, binary.LittleEndian, uint32(shapePolygon))
			_ = binary.Write(&content, binary.LittleEndian, rMinX)
			_ = binary.Write(&content, binary.LittleEndian, rMinY)
			_ = binary.Write(&content, binary.LittleEndian, rMaxX)
			_ = binary.Write(&content, binary.LittleEndian, rMaxY)
			_ = binary.Write(&content, binary.LittleEndian, uint32(len(parts)))
			_ = binary.Write(&content, binary.LittleEndian, uint32(len(points)))
			for _, p := range parts {
				_ = binary.Write(&content, binary.LittleEndian, p)
			}
			for _, pt := range points {
				_ = binary.Write(&content, binary.LittleEndian, pt[0])
				_ = binary.Write(&content, binary.LittleEndian, pt[1])
			}
		}

		_ = binary.Write(&body, binary.BigEndian, uint32(i+1))
		_ = binary.Write(&body, binary.BigEndian, uint32(content.Len()/2))
		body.Write(content.Bytes())
	}

	if math.IsInf(minX, 1) {
		minX, minY, maxX, maxY = 0, 0, 0, 0
	}

	var hdr [shapefileHeaderLen]byte
	binary.BigEndian.PutUint32(hdr[0:4], shapefileCode)
	binary.BigEndian.PutUint32(hdr[24:28], uint32((shapefileHeaderLen+body.Len())/2))
	binary.LittleEndian.PutUint32(hdr[28:32], shapefileVersion)
	binary.LittleEndian.PutUint32(hdr[32:36], shapePolygon)
	binary.LittleEndian.PutUint64(hdr[36:44], math.Float64bits(minX))
	binary.LittleEndian.PutUint64(hdr[44:52], math.Float64bits(minY))
	binary.LittleEndian.PutUint64(hdr[52:60], math.Float64bits(maxX))
	binary.LittleEndian.PutUint64(hdr[60:68], math.Float64bits(maxY))

	out := make([]byte, 0, shapefileHeaderLen+body.Len())
	out = append(out, hdr[:]...)
	out = append(out, body.Bytes()...)
	return out
}

func TestRasterizeShapefileZipDirAndGeoJSON(t *testing.T) {
	proj := geo.NewLAEA(53.5, 8.0)

	// Analysis area covers roughly [7.8..8.2, 53.3..53.7].
	// Land tile covers southern half [7.8..8.2, 53.3..53.5] with a small hole inside,
	// plus a distant out-of-bbox tile that must be skipped, plus a Null record.
	landTile := [][][2]float64{
		{{7.8, 53.3}, {8.2, 53.3}, {8.2, 53.5}, {7.8, 53.5}, {7.8, 53.3}},
		{{7.95, 53.35}, {8.05, 53.35}, {8.05, 53.45}, {7.95, 53.45}, {7.95, 53.35}},
	}
	distantTile := [][][2]float64{
		{{20.0, 40.0}, {21.0, 40.0}, {21.0, 41.0}, {20.0, 41.0}, {20.0, 40.0}},
	}

	shpBytes := buildTestShapefile(t, []testShpRecord{
		{nullRec: true},
		{rings: distantTile},
		{rings: landTile},
	})

	dir := t.TempDir()
	shpDir := filepath.Join(dir, "land-polygons-split-4326")
	if err := os.MkdirAll(shpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shpPath := filepath.Join(shpDir, "land_polygons.shp")
	if err := os.WriteFile(shpPath, shpBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(dir, "land-polygons-split-4326.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("land-polygons-split-4326/land_polygons.shp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(shpBytes); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	geojsonPath := filepath.Join(dir, "land.geojson")
	geojsonContent := `{
		"type": "FeatureCollection",
		"features": [
			{
				"type": "Feature",
				"geometry": {
					"type": "Polygon",
					"coordinates": [
						[[20.0, 40.0], [21.0, 40.0], [21.0, 41.0], [20.0, 41.0], [20.0, 40.0]]
					]
				}
			},
			{
				"type": "Feature",
				"geometry": {
					"type": "Polygon",
					"coordinates": [
						[[7.8, 53.3], [8.2, 53.3], [8.2, 53.5], [7.8, 53.5], [7.8, 53.3]],
						[[7.95, 53.35], [8.05, 53.35], [8.05, 53.45], [7.95, 53.45], [7.95, 53.35]]
					]
				}
			}
		]
	}`
	if err := os.WriteFile(geojsonPath, []byte(geojsonContent), 0o600); err != nil {
		t.Fatal(err)
	}

	xMin, yMin := proj.Forward(53.25, 7.75)
	xMax, yMax := proj.Forward(53.75, 8.25)
	res := 250.0
	invRes := 1.0 / res
	gridW := int(math.Ceil((xMax-xMin)/res)) + 4
	gridH := int(math.Ceil((yMax-yMin)/res)) + 4
	originX := xMin - 2*res
	originY := yMin - 2*res
	qBox := QueryBBoxFromLAEA(xMin, yMin, xMax, yMax, proj)

	toPix := func(lat, lon float64) (int, int) {
		mx, my := proj.Forward(lat, lon)
		return int(math.Floor((mx - originX) * invRes)), int(math.Floor((my - originY) * invRes))
	}
	landX, landY := toPix(53.32, 7.85)
	holeX, holeY := toPix(53.40, 8.00)
	seaX, seaY := toPix(53.60, 8.00)

	for _, src := range []struct {
		name string
		path string
	}{
		{"shp", shpPath},
		{"zip", zipPath},
		{"dir", shpDir},
		{"geojson", geojsonPath},
	} {
		t.Run(src.name, func(t *testing.T) {
			if err := Validate(src.path); err != nil {
				t.Fatalf("Validate(%q) failed: %v", src.path, err)
			}
			bs := raster.NewBitset(gridW, gridH)
			stats, err := Rasterize(src.path, qBox, proj, bs, originX, originY, invRes)
			if err != nil {
				t.Fatalf("Rasterize(%q) failed: %v", src.path, err)
			}
			if stats.TotalRecords != 2 || stats.MatchedRecords != 1 || stats.RasterizedRings != 2 {
				t.Fatalf("unexpected stats = %+v, want Total=2 Matched=1 Rings=2", stats)
			}
			if !bs.Get(landX, landY) {
				t.Fatalf("expected land pixel (%d,%d) to be set", landX, landY)
			}
			if bs.Get(holeX, holeY) {
				t.Fatalf("expected inner hole pixel (%d,%d) to be clear", holeX, holeY)
			}
			if bs.Get(seaX, seaY) {
				t.Fatalf("expected sea pixel (%d,%d) to be clear", seaX, seaY)
			}
		})
	}
}

func TestValidateRejectsEPSG3857(t *testing.T) {
	mercator := [][][2]float64{
		{{800000, 7000000}, {900000, 7000000}, {900000, 7100000}, {800000, 7100000}, {800000, 7000000}},
	}
	shpBytes := buildTestShapefile(t, []testShpRecord{{rings: mercator}})
	p := filepath.Join(t.TempDir(), "mercator.shp")
	if err := os.WriteFile(p, shpBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(p); err == nil {
		t.Fatalf("Validate(EPSG:3857) succeeded, expected error")
	}
}

func TestBBoxIntersectsAndQueryBBox(t *testing.T) {
	b := BBox{MinLon: 5, MinLat: 50, MaxLon: 10, MaxLat: 55}
	if !b.Intersects(8, 52, 12, 56) {
		t.Fatalf("expected overlapping bbox to intersect")
	}
	if !b.Intersects(10, 55, 12, 58) {
		t.Fatalf("expected edge-touching bbox to intersect")
	}
	if b.Intersects(10.1, 51, 12, 54) || b.Intersects(6, 55.1, 9, 58) {
		t.Fatalf("expected disjoint bbox not to intersect")
	}

	// Mid-latitude query box includes padding around origin.
	proj := geo.NewLAEA(53.5, 8.0)
	qb := QueryBBoxFromLAEA(-10000, -10000, 10000, 10000, proj)
	if !(qb.MinLon < 8.0 && qb.MaxLon > 8.0 && qb.MinLat < 53.5 && qb.MaxLat > 53.5) {
		t.Fatalf("mid-latitude QueryBBoxFromLAEA = %+v", qb)
	}

	// Polar box crossing the pole wraps longitude to [-180, 180] and clamps latitude to 90.
	projPole := geo.NewLAEA(89.9, 0.0)
	polar := QueryBBoxFromLAEA(-50000, -50000, 50000, 50000, projPole)
	if polar.MinLon != -180.0 || polar.MaxLon != 180.0 || polar.MaxLat != 90.0 {
		t.Fatalf("polar QueryBBoxFromLAEA = %+v, want lon=[-180,180] MaxLat=90", polar)
	}

	// South-polar box centered exactly at -90° must clamp MinLat=-90 without triggering North Pole antipode.
	projSouth := geo.NewLAEA(-90.0, 0.0)
	south := QueryBBoxFromLAEA(-50000, -50000, 50000, 50000, projSouth)
	if south.MinLon != -180.0 || south.MaxLon != 180.0 || south.MinLat != -90.0 || south.MaxLat >= 0.0 {
		t.Fatalf("south polar QueryBBoxFromLAEA = %+v, want lon=[-180,180] MinLat=-90 MaxLat<0", south)
	}
}

func TestProjectRing(t *testing.T) {
	proj := geo.NewLAEA(53.0, 8.0)
	if got := projectRing([][2]float64{{8, 53}, {8.01, 53}}, proj); got != nil {
		t.Fatalf("projectRing(<3 pts) = %+v, want nil", got)
	}

	// Open short ring closes cleanly without extra subdivision.
	openShort := projectRing([][2]float64{{8.0, 53.0}, {8.01, 53.0}, {8.0, 53.01}}, proj)
	if len(openShort) != 4 || openShort[0] != openShort[len(openShort)-1] {
		t.Fatalf("projectRing(open short) len=%d closed=%t", len(openShort), openShort[0] == openShort[len(openShort)-1])
	}

	// Edge exceeding maxDegStep (0.02 deg) is subdivided.
	longEdge := projectRing([][2]float64{{8.0, 53.0}, {8.1, 53.0}, {8.1, 53.01}, {8.0, 53.0}}, proj)
	if len(longEdge) <= 4 || longEdge[0] != longEdge[len(longEdge)-1] {
		t.Fatalf("projectRing(long edge) did not subdivide: len=%d", len(longEdge))
	}
}

func TestValidateAndRasterizeEdgeCases(t *testing.T) {
	dir := t.TempDir()
	validShp := buildTestShapefile(t, nil)

	// Missing path, unsupported extension, empty GeoJSON.
	if err := Validate(filepath.Join(dir, "nope.shp")); err == nil {
		t.Fatalf("Validate(missing) succeeded")
	}
	txtPath := filepath.Join(dir, "land.txt")
	if err := os.WriteFile(txtPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(txtPath); err == nil {
		t.Fatalf("Validate(.txt) succeeded")
	}
	emptyGeo := filepath.Join(dir, "empty.geojson")
	if err := os.WriteFile(emptyGeo, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(emptyGeo); err == nil {
		t.Fatalf("Validate(empty .geojson) succeeded")
	}

	// Directory resolution: 0 .shp -> error, 1 custom .shp -> ok, 2 .shp -> error.
	emptyDir := filepath.Join(dir, "empty-dir")
	if err := os.MkdirAll(emptyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Validate(emptyDir); err == nil {
		t.Fatalf("Validate(empty dir) succeeded")
	}
	singleDir := filepath.Join(dir, "single-dir")
	if err := os.MkdirAll(singleDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(singleDir, "custom.shp"), validShp, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(singleDir); err != nil {
		t.Fatalf("Validate(single custom .shp dir) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(singleDir, "second.shp"), validShp, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(singleDir); err == nil {
		t.Fatalf("Validate(multiple .shp dir) succeeded")
	}

	// Corrupt shapefile headers: invalid magic code and unsupported geometry type.
	badMagic := append([]byte(nil), validShp...)
	binary.BigEndian.PutUint32(badMagic[0:4], 1234)
	badMagicPath := filepath.Join(dir, "bad_magic.shp")
	if err := os.WriteFile(badMagicPath, badMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(badMagicPath); err == nil {
		t.Fatalf("Validate(bad magic) succeeded")
	}

	badType := append([]byte(nil), validShp...)
	binary.LittleEndian.PutUint32(badType[32:36], 1) // Point
	badTypePath := filepath.Join(dir, "bad_type.shp")
	if err := os.WriteFile(badTypePath, badType, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(badTypePath); err == nil {
		t.Fatalf("Validate(Point shapefile) succeeded")
	}

	// ZIP containing __MACOSX metadata plus valid .shp succeeds; ZIP without .shp fails.
	macZip := filepath.Join(dir, "macosx.zip")
	zf, err := os.Create(macZip)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	if w, werr := zw.Create("__MACOSX/._land_polygons.shp"); werr != nil {
		t.Fatal(werr)
	} else if _, werr = w.Write([]byte("junk")); werr != nil {
		t.Fatal(werr)
	}
	if w, werr := zw.Create("data/regions.shp"); werr != nil {
		t.Fatal(werr)
	} else if _, werr = w.Write(validShp); werr != nil {
		t.Fatal(werr)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Validate(macZip); err != nil {
		t.Fatalf("Validate(macosx zip) failed: %v", err)
	}

	// Empty shapefile and disjoint-header shapefile return zero stats cleanly.
	proj := geo.NewLAEA(53.5, 8.0)
	qBox := BBox{MinLon: 7.5, MinLat: 53.0, MaxLon: 8.5, MaxLat: 54.0}
	bs := raster.NewBitset(32, 32)
	emptyShpPath := filepath.Join(dir, "empty.shp")
	if err := os.WriteFile(emptyShpPath, validShp, 0o600); err != nil {
		t.Fatal(err)
	}
	if st, rerr := Rasterize(emptyShpPath, qBox, proj, bs, -1000, -1000, 0.01); rerr != nil || st != (Stats{}) {
		t.Fatalf("Rasterize(empty.shp) = (%+v, %v), want zero stats and nil err", st, rerr)
	}

	distantShp := buildTestShapefile(t, []testShpRecord{
		{rings: [][][2]float64{{{20, 40}, {21, 40}, {21, 41}, {20, 41}, {20, 40}}}},
	})
	distantPath := filepath.Join(dir, "distant.shp")
	if err := os.WriteFile(distantPath, distantShp, 0o600); err != nil {
		t.Fatal(err)
	}
	if st, rerr := Rasterize(distantPath, qBox, proj, bs, -1000, -1000, 0.01); rerr != nil || st != (Stats{}) {
		t.Fatalf("Rasterize(disjoint header) = (%+v, %v), want zero stats and nil err", st, rerr)
	}

	// GeoJSON MultiPolygon, null geometry, Point ignored, plus EPSG:3857 & invalid JSON errors.
	multiGeo := filepath.Join(dir, "multi.geojson")
	multiBody := `{
		"type": "FeatureCollection",
		"features": [
			{"type": "Feature", "geometry": null},
			{"type": "Feature", "geometry": {"type": "Point", "coordinates": [8.0, 53.5]}},
			{"type": "Feature", "geometry": {
				"type": "MultiPolygon",
				"coordinates": [
					[[[7.9, 53.4], [8.1, 53.4], [8.1, 53.6], [7.9, 53.6], [7.9, 53.4]]]
				]
			}}
		]
	}`
	if err := os.WriteFile(multiGeo, []byte(multiBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, rerr := Rasterize(multiGeo, qBox, proj, bs, -1000, -1000, 0.01); rerr != nil || st.MatchedRecords != 1 {
		t.Fatalf("Rasterize(MultiPolygon) = (%+v, %v), want MatchedRecords=1", st, rerr)
	}

	badGeo := filepath.Join(dir, "bad.geojson")
	if err := os.WriteFile(badGeo, []byte(`{"type":"Polygon","coordinates":[[[900000,7000000],[910000,7000000],[910000,7010000],[900000,7000000]]]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, rerr := Rasterize(badGeo, qBox, proj, bs, -1000, -1000, 0.01); rerr == nil {
		t.Fatalf("Rasterize(EPSG:3857 GeoJSON) succeeded, expected error")
	}
}

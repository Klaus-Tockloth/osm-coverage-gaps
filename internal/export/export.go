package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"

	"osm-area-gaps/internal/gaps"
)

/*
OSMURL formats an OpenStreetMap web slippy-map link centered on the given gap centroid at zoom level 15.
Coordinates are rounded to five decimal places for concise shareable links in CLI summaries, GeoJSON properties, and CSV reports.
Mappers can open the returned URL directly in a browser to inspect imagery and existing ground coverage at the gap location.
*/
func OSMURL(g gaps.Result) string {
	return fmt.Sprintf("https://www.openstreetmap.org/#map=15/%.5f/%.5f", g.CenterLat, g.CenterLon)
}

/*
JOSMURL builds a local JOSM RemoteControl load_and_zoom request URL covering the gap bounding box.
It encodes the minimum and maximum geographic bounds with six decimal places so JOSM downloads the exact area of interest on localhost port 8111.
This enables one-click workflows from QGIS or spreadsheet tables directly into the JOSM editor.
*/
func JOSMURL(g gaps.Result) string {
	return fmt.Sprintf(
		"http://127.0.0.1:8111/load_and_zoom?left=%.6f&right=%.6f&top=%.6f&bottom=%.6f",
		g.MinLon, g.MaxLon, g.MaxLat, g.MinLat,
	)
}

/*
WriteGeoJSON serializes the ranked gap polygons and metadata into an RFC 7946 GeoJSON FeatureCollection file at path.
Each feature contains a Polygon geometry with exterior and hole rings alongside rank, area in hectares, pixel count, centroid coordinates, and OSM and JOSM links.
HTML escaping is disabled on the JSON encoder so query parameter ampersands in JOSM RemoteControl URLs remain intact.
The function creates or truncates the output file and returns any I/O or encoding error encountered.
*/
func WriteGeoJSON(path string, results []gaps.Result) (err error) {
	type geoProps struct {
		Rank      int     `json:"rank"`
		AreaHa    float64 `json:"area_ha"`
		Pixels    int     `json:"pixels"`
		CenterLat float64 `json:"center_lat"`
		CenterLon float64 `json:"center_lon"`
		OSMURL    string  `json:"osm_url"`
		JOSMURL   string  `json:"josm_url"`
	}
	type geoGeom struct {
		Type        string         `json:"type"`
		Coordinates [][][2]float64 `json:"coordinates"`
	}
	type geoFeature struct {
		Type       string   `json:"type"`
		Properties geoProps `json:"properties"`
		Geometry   geoGeom  `json:"geometry"`
	}
	type geoCollection struct {
		Type     string       `json:"type"`
		Features []geoFeature `json:"features"`
	}

	fc := geoCollection{
		Type:     "FeatureCollection",
		Features: make([]geoFeature, len(results)),
	}
	for i, g := range results {
		fc.Features[i] = geoFeature{
			Type: "Feature",
			Properties: geoProps{
				Rank:      g.Rank,
				AreaHa:    math.Round(g.AreaHa*100) / 100,
				Pixels:    g.Pixels,
				CenterLat: g.CenterLat,
				CenterLon: g.CenterLon,
				OSMURL:    OSMURL(g),
				JOSMURL:   JOSMURL(g),
			},
			Geometry: geoGeom{
				Type:        "Polygon",
				Coordinates: g.Rings,
			},
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(fc)
}

/*
WriteCSV exports all ranked gap records as a comma-separated table suitable for spreadsheets and batch mapping task lists.
It writes a fixed header row followed by one formatted record per gap containing rank, area in hectares, pixel count, centroid coordinates, and OSM and JOSM URLs.
The writer is explicitly flushed before closing so any buffered write errors are propagated to the caller.
*/
func WriteCSV(path string, results []gaps.Result) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	w := csv.NewWriter(f)
	if werr := w.Write([]string{"rank", "area_ha", "pixels", "center_lat", "center_lon", "osm_url", "josm_url"}); werr != nil {
		return werr
	}
	for _, g := range results {
		row := []string{
			strconv.Itoa(g.Rank),
			fmt.Sprintf("%.2f", g.AreaHa),
			strconv.Itoa(g.Pixels),
			fmt.Sprintf("%.6f", g.CenterLat),
			fmt.Sprintf("%.6f", g.CenterLon),
			OSMURL(g),
			JOSMURL(g),
		}
		if werr := w.Write(row); werr != nil {
			return werr
		}
	}
	w.Flush()
	return w.Error()
}

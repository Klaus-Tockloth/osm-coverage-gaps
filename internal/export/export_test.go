package export

import (
	"encoding/csv"
	"encoding/json"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"osm-area-gaps/internal/gaps"
	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/raster"
)

func TestURLFormatting(t *testing.T) {
	g := gaps.Result{
		CenterLat: 51.9612346,
		CenterLon: -7.6289012,
		MinLat:    51.95,
		MaxLat:    51.97,
		MinLon:    -7.64,
		MaxLon:    -7.61,
	}
	wantOSM := "https://www.openstreetmap.org/#map=15/51.96123/-7.62890"
	if got := OSMURL(g); got != wantOSM {
		t.Fatalf("OSMURL() = %q, want %q", got, wantOSM)
	}
	wantJOSM := "http://127.0.0.1:8111/load_and_zoom?left=-7.640000&right=-7.610000&top=51.970000&bottom=51.950000"
	if got := JOSMURL(g); got != wantJOSM {
		t.Fatalf("JOSMURL() = %q, want %q", got, wantJOSM)
	}
}

func TestWriteGeoJSON(t *testing.T) {
	dir := t.TempDir()
	sample := []gaps.Result{
		{
			Rank:      1,
			Pixels:    123,
			AreaHa:    12.3456,
			CenterLat: 51.961235,
			CenterLon: 7.628901,
			MinLat:    51.95,
			MaxLat:    51.97,
			MinLon:    7.61,
			MaxLon:    7.64,
			Rings: [][][2]float64{
				{{7.61, 51.95}, {7.64, 51.95}, {7.64, 51.97}, {7.61, 51.97}, {7.61, 51.95}},
				{{7.62, 51.955}, {7.62, 51.965}, {7.63, 51.965}, {7.63, 51.955}, {7.62, 51.955}},
			},
		},
	}

	path := filepath.Join(dir, "gaps.geojson")
	if err := WriteGeoJSON(path, sample); err != nil {
		t.Fatalf("WriteGeoJSON() error: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u0026`) || !strings.Contains(string(raw), "&right=") {
		t.Fatalf("GeoJSON escaped ampersands in josm_url: %s", string(raw))
	}

	var parsed struct {
		Type     string `json:"type"`
		Features []struct {
			Type       string `json:"type"`
			Properties struct {
				Rank   int     `json:"rank"`
				AreaHa float64 `json:"area_ha"`
				Pixels int     `json:"pixels"`
			} `json:"properties"`
			Geometry struct {
				Type        string         `json:"type"`
				Coordinates [][][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid GeoJSON: %v", err)
	}
	if parsed.Type != "FeatureCollection" || len(parsed.Features) != 1 {
		t.Fatalf("unexpected FeatureCollection: %+v", parsed)
	}
	f := parsed.Features[0]
	if f.Properties.Rank != 1 || f.Properties.AreaHa != 12.35 || f.Properties.Pixels != 123 {
		t.Fatalf("unexpected GeoJSON properties: %+v", f.Properties)
	}
	if f.Geometry.Type != "Polygon" || len(f.Geometry.Coordinates) != 2 {
		t.Fatalf("unexpected GeoJSON geometry: %+v", f.Geometry)
	}

	// Empty slice must serialize as [] instead of null.
	emptyPath := filepath.Join(dir, "empty.geojson")
	if err := WriteGeoJSON(emptyPath, nil); err != nil {
		t.Fatalf("WriteGeoJSON(nil) error: %v", err)
	}
	emptyRaw, err := os.ReadFile(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emptyRaw), `"features":[]`) {
		t.Fatalf("empty GeoJSON missing empty array: %s", string(emptyRaw))
	}

	// Invalid destination returns error.
	if err := WriteGeoJSON(filepath.Join(dir, "missing", "out.geojson"), nil); err == nil {
		t.Fatalf("expected error for invalid GeoJSON path")
	}
}

func TestWriteCSV(t *testing.T) {
	dir := t.TempDir()
	g := gaps.Result{
		Rank:      1,
		Pixels:    123,
		AreaHa:    12.3456,
		CenterLat: 51.961235,
		CenterLon: 7.628901,
		MinLat:    51.95,
		MaxLat:    51.97,
		MinLon:    7.61,
		MaxLon:    7.64,
	}

	path := filepath.Join(dir, "gaps.csv")
	if err := WriteCSV(path, []gaps.Result{g}); err != nil {
		t.Fatalf("WriteCSV() error: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("CSV rows = %d, want 2", len(rows))
	}
	wantRow := []string{"1", "12.35", "123", "51.961235", "7.628901", OSMURL(g), JOSMURL(g)}
	for i := range wantRow {
		if rows[1][i] != wantRow[i] {
			t.Fatalf("CSV col %d = %q, want %q", i, rows[1][i], wantRow[i])
		}
	}

	// Empty slice writes header row only.
	emptyPath := filepath.Join(dir, "empty.csv")
	if err := WriteCSV(emptyPath, nil); err != nil {
		t.Fatalf("WriteCSV(nil) error: %v", err)
	}
	ef, err := os.Open(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	emptyRows, err := csv.NewReader(ef).ReadAll()
	if cerr := ef.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyRows) != 1 || emptyRows[0][0] != "rank" || emptyRows[0][6] != "josm_url" {
		t.Fatalf("unexpected empty CSV header: %+v", emptyRows)
	}

	if err := WriteCSV(filepath.Join(dir, "missing", "out.csv"), nil); err == nil {
		t.Fatalf("expected error for invalid CSV path")
	}
}

func TestPreviewMap(t *testing.T) {
	// Scaling dimensions, including extreme aspect ratio clamp to >= 1.
	p1 := NewPreviewMap(100, 50)
	if p1.W != 100 || p1.H != 50 {
		t.Fatalf("NewPreviewMap(100,50) = (%d,%d), want (100,50)", p1.W, p1.H)
	}
	p2 := NewPreviewMap(3600, 1800)
	if p2.W != 1800 || p2.H != 900 {
		t.Fatalf("NewPreviewMap(3600,1800) = (%d,%d), want (1800,900)", p2.W, p2.H)
	}
	p3 := NewPreviewMap(10000, 1)
	if p3.W != 1800 || p3.H != 1 {
		t.Fatalf("NewPreviewMap(10000,1) = (%d,%d), want (1800,1)", p3.W, p3.H)
	}

	// Verify north-up vertical inversion, multi-word painting, OOB cell guard, and PNG round-trip.
	bs := raster.NewBitset(80, 10)
	bs.SetSpan(0, 0, 0, true)   // raster bottom-left -> PNG (0, 9)
	bs.SetSpan(9, 70, 70, true) // raster top, word 1 -> PNG (70, 0)

	green := color.RGBA{45, 92, 62, 255}
	red := color.RGBA{235, 55, 45, 255}
	bg := color.RGBA{24, 28, 36, 255}

	pm := NewPreviewMap(80, 10)
	pm.PaintBitset(bs, green)
	pm.PaintCells([]geo.Cell{
		{X: 70, Y: 9},
		{X: -1, Y: 0},
		{X: 80, Y: 0},
		{X: 0, Y: 10},
	}, red)

	dir := t.TempDir()
	pngPath := filepath.Join(dir, "preview.png")
	if err := pm.Save(pngPath); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	f, err := os.Open(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatalf("png.Decode() error: %v", err)
	}
	if img.Bounds().Dx() != 80 || img.Bounds().Dy() != 10 {
		t.Fatalf("decoded PNG size = %v, want 80x10", img.Bounds())
	}
	if got := color.RGBAModel.Convert(img.At(0, 9)).(color.RGBA); got != green {
		t.Fatalf("pixel (0,9) = %+v, want green %+v", got, green)
	}
	if got := color.RGBAModel.Convert(img.At(70, 0)).(color.RGBA); got != red {
		t.Fatalf("pixel (70,0) = %+v, want red %+v", got, red)
	}
	if got := color.RGBAModel.Convert(img.At(10, 5)).(color.RGBA); got != bg {
		t.Fatalf("pixel (10,5) = %+v, want background %+v", got, bg)
	}

	if err := pm.Save(filepath.Join(dir, "missing", "preview.png")); err == nil {
		t.Fatalf("expected error for invalid PNG path")
	}
}

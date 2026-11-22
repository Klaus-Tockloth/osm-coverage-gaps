package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"osm-area-gaps/internal/gaps"
	"osm-area-gaps/internal/osmtags"
)

func TestFormatFileSize(t *testing.T) {
	dir := t.TempDir()
	if got := formatFileSize(filepath.Join(dir, "missing")); got != "unknown size" {
		t.Fatalf("formatFileSize(missing) = %q, want %q", got, "unknown size")
	}

	cases := []struct {
		name string
		size int64
		want string
	}{
		{"bytes", 512, "512 B"},
		{"kilobytes", 2048, "2.0 KB"},
		{"megabytes", 2 << 20, "2.00 MB"},
	}
	for _, tc := range cases {
		p := filepath.Join(dir, tc.name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(tc.size); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if got := formatFileSize(p); got != tc.want {
			t.Fatalf("formatFileSize(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPrintSettings(t *testing.T) {
	var defBuf bytes.Buffer
	defCfg := &config{
		pbfPath:          "in.osm.pbf",
		outGeoJSON:       "out.geojson",
		outCSV:           "out.csv",
		outPNG:           "out.png",
		outLog:           "out.log",
		resMeters:        20,
		minHa:            5,
		opening:          0,
		workers:          4,
		noDefaultIgnores: true,
		classifier:       osmtags.NewClassifier(false, nil),
	}
	printSettings(&defBuf, defCfg, 125)
	defOut := defBuf.String()
	for _, want := range []string{`"" (disabled)`, "0 (auto)", `"" (auto)`, "0 px (disabled)", "0 (all)", "disabled", "none"} {
		if !strings.Contains(defOut, want) {
			t.Fatalf("default printSettings missing %q:\n%s", want, defOut)
		}
	}

	rule, err := osmtags.ParseRule("landuse=military>50")
	if err != nil {
		t.Fatal(err)
	}
	var expBuf bytes.Buffer
	expCfg := &config{
		pbfPath:          "in.osm.pbf",
		landPolygonsPath: "land_polygons.shp",
		outGeoJSON:       "out.geojson",
		outCSV:           "out.csv",
		outPNG:           "out.png",
		outLog:           "out.log",
		boundaryRel:      51477,
		boundaryName:     "Bayern",
		resMeters:        10,
		minHa:            2.5,
		opening:          2,
		workers:          8,
		maxFeatures:      100,
		classifier:       osmtags.NewClassifier(true, []osmtags.Rule{rule}),
	}
	printSettings(&expBuf, expCfg, 250)
	expOut := expBuf.String()
	for _, want := range []string{"land_polygons.shp", "51477", `"Bayern"`, "2 px (~40 m width)", "100", "enabled (", "landuse=military>50"} {
		if !strings.Contains(expOut, want) {
			t.Fatalf("explicit printSettings missing %q:\n%s", want, expOut)
		}
	}
}

func TestPrintSummary(t *testing.T) {
	dir := t.TempDir()
	cfg := &config{
		outGeoJSON: filepath.Join(dir, "out.geojson"),
		outCSV:     filepath.Join(dir, "out.csv"),
		outPNG:     filepath.Join(dir, "out.png"),
		outLog:     filepath.Join(dir, "out.log"),
	}

	// Empty results, zero boundary pixels, zero timestamp, opening disabled.
	var emptyBuf bytes.Buffer
	printSummary(&emptyBuf, cfg, &summaryStats{
		resMeters: 10,
		minHa:     5.0,
		opening:   0,
	}, nil)
	emptyOut := emptyBuf.String()
	if strings.Contains(emptyOut, "NaN") || strings.Contains(emptyOut, "Inf") {
		t.Fatalf("empty summary contains NaN/Inf:\n%s", emptyOut)
	}
	for _, want := range []string{"unknown", "opening disabled", "No unmapped gaps >= 5.00 ha were found."} {
		if !strings.Contains(emptyOut, want) {
			t.Fatalf("empty summary missing %q:\n%s", want, emptyOut)
		}
	}
	if strings.Contains(emptyOut, "Excluded sea area") {
		t.Fatalf("summary unexpectedly included Excluded sea area without landPolygonsPath")
	}

	// 26 results (even count for median + top-25 truncation), sea masking enabled, maxFeatures cap.
	for _, p := range []string{cfg.outGeoJSON, cfg.outCSV, cfg.outPNG, cfg.outLog} {
		if err := os.WriteFile(p, []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.landPolygonsPath = "land.shp"
	cfg.maxFeatures = 10

	results := make([]gaps.Result, 26)
	for i := range results {
		area := 10.0
		switch {
		case i == 0:
			area = 100.0
		case i <= 12:
			area = 20.0
		}
		results[i] = gaps.Result{
			Rank:      i + 1,
			AreaHa:    area,
			CenterLat: 51.0,
			CenterLon: 10.0,
		}
	}

	flushed := false
	var fullBuf bytes.Buffer
	printSummary(&fullBuf, cfg, &summaryStats{
		runtime:          1500 * time.Millisecond,
		pbfTimestamp:     time.Date(2026, 11, 20, 21, 21, 21, 0, time.UTC),
		boundaryDesc:     "Relation 51477",
		boundaryRings:    1,
		gridW:            100,
		gridH:            100,
		resMeters:        10,
		minHa:            5.0,
		opening:          2,
		boundaryPixels:   8000,
		seaPixels:        2000,
		coveredPixels:    6000,
		unmappedRawPx:    2000,
		unmappedOpenedPx: 1500,
		exportedGaps:     10,
		previewW:         100,
		previewH:         100,
		allResults:       results,
	}, func() {
		flushed = true
	})

	if !flushed {
		t.Fatalf("flushLog callback was not invoked")
	}
	fullOut := fullBuf.String()
	for _, want := range []string{
		"2026-11-20T21:21:21Z",
		"Excluded sea area",
		"10 (limited by -max-out)",
		"100.00 / 18.08 / 15.00 / 10.00 ha",
		"Top 25 gaps (of 26 found):",
		"#25",
	} {
		if !strings.Contains(fullOut, want) {
			t.Fatalf("full summary missing %q:\n%s", want, fullOut)
		}
	}
	if strings.Contains(fullOut, "#26") {
		t.Fatalf("summary exceeded top-25 cap:\n%s", fullOut)
	}
}

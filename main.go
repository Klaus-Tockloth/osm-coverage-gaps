/*
Purpose:
- Detect unmapped surface areas (white spots) in OpenStreetMap .osm.pbf extracts.

Description:
- Rasterize administrative boundaries and mapped landcover or landuse polygons onto
  an equal-area LAEA bitset grid, optionally clip sea areas via external WGS84 land polygons,
  suppress narrow artifacts via morphological opening, and export remaining unmapped gaps
  as GeoJSON, CSV, PNG preview, and diagnostic log files.

Releases:
- v1.0.0 - 2026-11-22: Initial release.

Author:
- Klaus Tockloth.

Development:
- Concept, architecture, full integration, and verification by the author.
- Implementation substantially assisted by Google 'Gemini 4 Neon' (AI coding assistant).

Copyright:
- © 2026 | Klaus Tockloth.

License:
- MIT License

Contact:
- klaus.tockloth@googlemail.com
*/

// Package main implements the osm-coverage-gaps CLI tool for detecting unmapped surface areas in OSM PBF extracts.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"osm-area-gaps/internal/boundary"
	"osm-area-gaps/internal/export"
	"osm-area-gaps/internal/gaps"
	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/landmask"
	"osm-area-gaps/internal/osmtags"
	"osm-area-gaps/internal/pbf"
	"osm-area-gaps/internal/raster"
)

const (
	progName    = "osm-coverage-gaps"
	progVersion = "v1.0.0"
	progDate    = "2026-11-22"
)

var progTitle = os.Args[0]

// config holds all parsed CLI parameters, active tag classifier, and derived output file paths.
type config struct {
	pbfPath          string
	landPolygonsPath string
	outGeoJSON       string
	outCSV           string
	outPNG           string
	outLog           string
	boundaryRel      int64
	boundaryName     string
	resMeters        float64
	minHa            float64
	opening          int
	workers          int
	maxFeatures      int
	noDefaultIgnores bool
	ignoreRules      []osmtags.Rule
	classifier       *osmtags.Classifier
}

// rasterLossStats tracks missing coordinates, missing ways, open ring chains, and dropped polygons during rasterization.
type rasterLossStats struct {
	neededNodesSorted      bool
	missingCoordsPass3     int64
	standaloneMissingNodes int64
	standaloneDropped      int64
	condMissingNodes       int64
	condDropped            int64
	relMissingOuterWays    int64
	relMissingInnerWays    int64
	relIncompleteOuter     int64
	relIncompleteInner     int64
	relMissingOuterNodes   int64
	relMissingInnerNodes   int64
	relDroppedNoOuter      int64
	relPartialLoss         int64
}

// summaryStats aggregates runtime, coverage, sea-mask, exclusion, geometry loss, output file, and gap metrics for final reporting.
type summaryStats struct {
	runtime          time.Duration
	pbfTimestamp     time.Time
	boundaryDesc     string
	boundaryRings    int
	gridW            int
	gridH            int
	resMeters        float64
	minHa            float64
	opening          int
	boundaryPixels   int64
	seaPixels        int64
	coveredPixels    int64
	unmappedRawPx    int64
	unmappedOpenedPx int64
	ignoredByTag     int64
	ignoredByArea    int64
	losses           rasterLossStats
	exportedGaps     int
	previewW         int
	previewH         int
	allResults       []gaps.Result
}

/*
main serves as the command-line entry point for osm-coverage-gaps.
It parses and validates CLI arguments via parseConfig and executes the analysis pipeline under a background context.
Any pipeline failure is logged with full context before exiting the process with a fatal status.
*/
func main() {
	cfg := parseConfig()
	if err := run(context.Background(), cfg); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

/*
printUsage writes the command synopsis, all supported CLI flags with defaults, land-polygon download guidance, and concrete invocation examples to standard error.
The examples demonstrate every available command-line option across regional, nationwide, raw-analysis, overlay-exclusion, and coastal land-masking workflows.
This function is registered as the flag package usage callback so help requests and argument errors display complete invocation guidance.
*/
func printUsage() {
	out := flag.CommandLine.Output()
	_, _ = fmt.Fprintf(out, "%s %s (%s) — Detect unmapped surface areas (white spots) in OSM PBF extracts.\n\n",
		progName, progVersion, progDate)
	_, _ = fmt.Fprintf(out, "This program always generates %s.geojson, %s.csv, %s.png, and %s.log in the current working directory.\n\n",
		progName, progName, progName, progName)
	_, _ = fmt.Fprintf(out, "Usage:\n  %s -pbf <file.osm.pbf> [options]\n\n", progTitle)
	_, _ = fmt.Fprintf(out, "Options:\n")
	flag.PrintDefaults()
	_, _ = fmt.Fprintf(out, `
Land Polygons (-land-polygons):
  Coastal administrative boundaries include 12 nm sea areas that OSM does not model as closed water polygons.
  Without -land-polygons, sea areas remain inside the analysis boundary and are reported as unmapped.
  With -land-polygons, the analysis boundary is clipped to land and excluded sea areas are rendered blue in the PNG preview.

  Download daily pre-built WGS84 (EPSG:4326) land polygons from FOSSGIS / OSMData:
    Webpage : https://osmdata.openstreetmap.de/data/land-polygons.html
    Direct  : https://osmdata.openstreetmap.de/download/land-polygons-split-4326.zip

  Recommended setup (unzipped .shp enables fast seeking):
    curl -LO https://osmdata.openstreetmap.de/download/land-polygons-split-4326.zip
    unzip land-polygons-split-4326.zip
    -> pass: -land-polygons land-polygons-split-4326/land_polygons.shp

  Alternatively, pass the .zip archive directly (-land-polygons land-polygons-split-4326.zip),
  the extracted directory, or a clipped WGS84 GeoJSON file (.geojson).
`)
	_, _ = fmt.Fprintf(out, `
Examples (covering all available options):
  1) Run a minimal analysis with automatic boundary detection and default settings (-pbf).
     %s -pbf niedersachsen-latest.osm.pbf

  2) Analyze a federal state at high resolution with a smaller minimum gap threshold (-pbf, -res, -min-ha).
     %s -pbf bayern-latest.osm.pbf -res 10 -min-ha 2.5

  3) Analyze all of Germany using an explicit OSM relation ID with export and worker limits (-pbf, -boundary-rel, -res, -min-ha, -opening, -max-out, -workers).
     %s -pbf germany-latest.osm.pbf -boundary-rel 51477 -res 20 -min-ha 10 -opening 2 -max-out 500 -workers 8

  4) Analyze a specific subregion selected by its administrative boundary name (-pbf, -boundary-name, -res, -min-ha).
     %s -pbf nordrhein-westfalen-latest.osm.pbf -boundary-name "Münster" -res 5 -min-ha 1.0

  5) Run a raw gap analysis without morphological opening to retain narrow road corridors and slivers (-pbf, -opening, -res, -min-ha).
     %s -pbf saarland-latest.osm.pbf -opening 0 -res 5 -min-ha 0.5

  6) Ignore untyped military areas above 50 ha and airfields above 30 ha while keeping small barracks covered (-pbf, -res, -ignore-area).
     %s -pbf niedersachsen-latest.osm.pbf -res 10 -ignore-area "landuse=military>50" -ignore-area "aeroway=aerodrome>30"

  7) Exclude all military areas and wind farms via conjunctive tag rules (-pbf, -ignore-area).
     %s -pbf brandenburg-latest.osm.pbf -ignore-area landuse=military -ignore-area "power=plant&plant:source=wind"

  8) Disable built-in overlay defaults (military training ranges, winter sports, conservation) (-pbf, -no-default-ignores).
     %s -pbf bayern-latest.osm.pbf -no-default-ignores

  9) Analyze a coastal state while excluding sea areas with OSMData WGS84 land polygons (-pbf, -land-polygons, -res).
     %s -pbf niedersachsen-latest.osm.pbf -land-polygons land-polygons-split-4326/land_polygons.shp -res 10
`, progTitle, progTitle, progTitle, progTitle, progTitle, progTitle, progTitle, progTitle, progTitle)
}

/*
parseConfig defines and parses all command-line flags into a validated runtime configuration struct.
It attaches printUsage to the default flag set, assigns fixed GeoJSON, CSV, PNG, and log output filenames, and verifies that the mandatory PBF input path is provided.
Numeric thresholds, area-exclusion rule expressions, and any optional land-polygon source are validated immediately before starting Pass 1.
On invalid or missing arguments, the function prints diagnostics to standard error and terminates the process with a non-zero exit code.
*/
func parseConfig() *config {
	cfg := &config{
		outGeoJSON: progName + ".geojson",
		outCSV:     progName + ".csv",
		outPNG:     progName + ".png",
		outLog:     progName + ".log",
	}
	flag.Usage = printUsage
	flag.StringVar(&cfg.pbfPath, "pbf", "", "Specify the path to the input .osm.pbf file (required).")
	flag.StringVar(&cfg.landPolygonsPath, "land-polygons", "", "Specify optional WGS84 (EPSG:4326) land polygons (.shp, .zip, directory, or .geojson) from osmdata.openstreetmap.de to exclude sea areas.")
	flag.Int64Var(&cfg.boundaryRel, "boundary-rel", 0, "Specify an explicit OSM relation ID for the analysis boundary (e.g. 51477 for Germany).")
	flag.StringVar(&cfg.boundaryName, "boundary-name", "", "Specify an explicit administrative boundary name matched case-insensitively (e.g. \"Bayern\").")
	flag.Float64Var(&cfg.resMeters, "res", 20.0, "Set the raster pixel size in meters (e.g. 10 for a federal state, 20 for a country).")
	flag.Float64Var(&cfg.minHa, "min-ha", 5.0, "Set the minimum size of a reported unmapped gap in hectares.")
	flag.IntVar(&cfg.opening, "opening", 2, "Set the morphological opening radius in pixels to suppress narrow road corridors (0 disables opening).")
	flag.IntVar(&cfg.workers, "workers", runtime.NumCPU(), "Set the number of parallel worker goroutines.")
	flag.IntVar(&cfg.maxFeatures, "max-out", 0, "Limit the maximum number of exported gaps in GeoJSON and CSV outputs (0 exports all).")
	flag.Func("ignore-area", "Exclude matching area polygons from covered surfaces (repeatable; syntax: `rule` as key=value, key=*, k1=v1&k2=v2, or <match>><max_ha>, e.g. \"landuse=military>50\").", func(val string) error {
		rule, err := osmtags.ParseRule(val)
		if err != nil {
			return err
		}
		cfg.ignoreRules = append(cfg.ignoreRules, rule)
		return nil
	})
	flag.BoolVar(&cfg.noDefaultIgnores, "no-default-ignores", false, "Disable built-in overlay exclusions (military=training_area|danger_area|range, landuse=winter_sports|conservation).")
	flag.Parse()

	if cfg.pbfPath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Error: -pbf must be specified.")
		flag.Usage()
		os.Exit(2)
	}
	if math.IsNaN(cfg.resMeters) || math.IsInf(cfg.resMeters, 0) || cfg.resMeters <= 0 ||
		math.IsNaN(cfg.minHa) || math.IsInf(cfg.minHa, 0) || cfg.minHa <= 0 ||
		cfg.opening < 0 || cfg.workers < 1 || cfg.maxFeatures < 0 {
		log.Fatal("Invalid parameters: require finite -res > 0, finite -min-ha > 0, -opening >= 0, -workers >= 1, and -max-out >= 0.")
	}
	if cfg.landPolygonsPath != "" {
		if err := landmask.Validate(cfg.landPolygonsPath); err != nil {
			log.Fatalf("Invalid -land-polygons %q: %v", cfg.landPolygonsPath, err)
		}
	}
	cfg.classifier = osmtags.NewClassifier(!cfg.noDefaultIgnores, cfg.ignoreRules)
	return cfg
}

/*
printSettings outputs the program version, all active CLI options, effective exclusion rules, derived thresholds, and fixed output paths to out for verification at startup.
Implicit defaults such as automatic boundary selection, disabled land masking, disabled opening, or active built-in overlay exclusions are annotated explicitly for clarity.
Printing this summary before pass 1 lets operators catch misconfigured parameters immediately on large extracts.
*/
func printSettings(out io.Writer, cfg *config, minPixels int) {
	landPolygonsDesc := `"" (disabled)`
	if cfg.landPolygonsPath != "" {
		landPolygonsDesc = cfg.landPolygonsPath
	}
	boundaryRelDesc := "0 (auto)"
	if cfg.boundaryRel > 0 {
		boundaryRelDesc = fmt.Sprintf("%d", cfg.boundaryRel)
	}
	boundaryNameDesc := `"" (auto)`
	if cfg.boundaryName != "" {
		boundaryNameDesc = fmt.Sprintf("%q", cfg.boundaryName)
	}
	openingDesc := "0 px (disabled)"
	if cfg.opening > 0 {
		openingDesc = fmt.Sprintf("%d px (~%.0f m width)", cfg.opening, float64(cfg.opening*2)*cfg.resMeters)
	}
	maxOutDesc := "0 (all)"
	if cfg.maxFeatures > 0 {
		maxOutDesc = fmt.Sprintf("%d", cfg.maxFeatures)
	}
	defaultIgnoresDesc := fmt.Sprintf("enabled (%d built-in rules)", len(osmtags.DefaultIgnoreRules()))
	if cfg.noDefaultIgnores {
		defaultIgnoresDesc = "disabled"
	}
	activeRulesDesc := "none"
	if rules := cfg.classifier.Rules(); len(rules) > 0 {
		parts := make([]string, len(rules))
		for i, r := range rules {
			parts[i] = r.String()
		}
		activeRulesDesc = strings.Join(parts, ", ")
	}

	_, _ = fmt.Fprintf(out, "\n%s %s (%s)\n", progName, progVersion, progDate)
	_, _ = fmt.Fprintf(out, "\nSettings:\n")
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Input PBF (-pbf)", cfg.pbfPath)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Land polygons (-land-polygons)", landPolygonsDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Boundary relation (-boundary-rel)", boundaryRelDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Boundary name (-boundary-name)", boundaryNameDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %g m/px\n", "Pixel resolution (-res)", cfg.resMeters)
	_, _ = fmt.Fprintf(out, "  %-37s : %.2f ha (%d pixels)\n", "Minimum gap area (-min-ha)", cfg.minHa, minPixels)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Morphological opening (-opening)", openingDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %d\n", "Parallel workers (-workers)", cfg.workers)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Max exported gaps (-max-out)", maxOutDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Default ignores (-no-default-ignores)", defaultIgnoresDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Active ignore rules (-ignore-area)", activeRulesDesc)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Output GeoJSON", cfg.outGeoJSON)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Output CSV", cfg.outCSV)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n", "Output PNG", cfg.outPNG)
	_, _ = fmt.Fprintf(out, "  %-37s : %s\n\n", "Output log", cfg.outLog)
}

/*
formatFileSize inspects the file at path and returns its byte count formatted in human-readable binary units.
If the file cannot be queried via os.Stat, it returns a placeholder string instead of failing the summary report.
This helper is used to report generated GeoJSON, CSV, PNG, and log file sizes at program completion.
*/
func formatFileSize(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "unknown size"
	}
	size := fi.Size()
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case size >= gb:
		return fmt.Sprintf("%.2f GB", float64(size)/float64(gb))
	case size >= mb:
		return fmt.Sprintf("%.2f MB", float64(size)/float64(mb))
	case size >= kb:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(kb))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

/*
printSummary prints the final analysis statistics, sea-masking metrics, geometry loss counters, generated output files with sizes, and the top 25 detected unmapped gaps to out.
Area metrics cover total active analysis extent, optional excluded sea area, mapped surface coverage, raw and post-opening unmapped area, and aggregate statistics across all qualifying gaps.
Each listed gap includes its rank, area in hectares, WGS84 centroid coordinates, and a direct OpenStreetMap slippy-map link.
*/
func printSummary(out io.Writer, cfg *config, st *summaryStats, flushLog func()) {
	cellHa := (st.resMeters * st.resMeters) / 10000.0
	boundaryHa := float64(st.boundaryPixels) * cellHa
	seaHa := float64(st.seaPixels) * cellHa
	coveredHa := float64(st.coveredPixels) * cellHa
	unmappedRawHa := float64(st.unmappedRawPx) * cellHa
	unmappedOpenedHa := float64(st.unmappedOpenedPx) * cellHa

	pct := func(part, total int64) float64 {
		if total <= 0 {
			return 0
		}
		return 100.0 * float64(part) / float64(total)
	}

	var totalGapAreaHa float64
	for i := range st.allResults {
		totalGapAreaHa += st.allResults[i].AreaHa
	}
	gapAreaPct := 0.0
	if boundaryHa > 0 {
		gapAreaPct = 100.0 * totalGapAreaHa / boundaryHa
	}

	pbfTS := "unknown"
	if !st.pbfTimestamp.IsZero() {
		pbfTS = st.pbfTimestamp.UTC().Format(time.RFC3339)
	}

	totalRelMissingWays := st.losses.relMissingOuterWays + st.losses.relMissingInnerWays
	totalRelIncomplete := st.losses.relIncompleteOuter + st.losses.relIncompleteInner
	totalRelMissingNodes := st.losses.relMissingOuterNodes + st.losses.relMissingInnerNodes

	_, _ = fmt.Fprintf(out, "\nSummary:\n")
	_, _ = fmt.Fprintf(out, "  %-26s : %s\n", "Total runtime", st.runtime)
	_, _ = fmt.Fprintf(out, "  %-26s : %s\n", "PBF data timestamp", pbfTS)
	_, _ = fmt.Fprintf(out, "  %-26s : %s (%d rings)\n", "Analysis boundary", st.boundaryDesc, st.boundaryRings)
	_, _ = fmt.Fprintf(out, "  %-26s : %d x %d px (%g m/px)\n", "Raster dimensions", st.gridW, st.gridH, st.resMeters)
	_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f km²)\n", "Analysis area", boundaryHa, boundaryHa/100.0)
	if cfg.landPolygonsPath != "" {
		rawBoundaryPx := st.boundaryPixels + st.seaPixels
		_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f km², %.2f%% of boundary)\n",
			"Excluded sea area", seaHa, seaHa/100.0, pct(st.seaPixels, rawBoundaryPx))
	}
	_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f%%)\n", "Covered area", coveredHa, pct(st.coveredPixels, st.boundaryPixels))
	_, _ = fmt.Fprintf(out, "  %-26s : %d by tag, %d by area\n", "Ignored areas", st.ignoredByTag, st.ignoredByArea)
	_, _ = fmt.Fprintf(out, "  %-26s : %d\n", "Missing nodes (Pass 3)", st.losses.missingCoordsPass3)
	_, _ = fmt.Fprintf(out, "  %-26s : %d standalone, %d conditional\n", "Dropped ways (missing)", st.losses.standaloneDropped, st.losses.condDropped)
	_, _ = fmt.Fprintf(out, "  %-26s : %d (%d partial loss)\n", "Dropped rels (no outer)", st.losses.relDroppedNoOuter, st.losses.relPartialLoss)
	_, _ = fmt.Fprintf(out, "  %-26s : %d/%d/%d\n", "Rel losses (way/inc/node)", totalRelMissingWays, totalRelIncomplete, totalRelMissingNodes)
	_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f%%)\n", "Unmapped area (raw)", unmappedRawHa, pct(st.unmappedRawPx, st.boundaryPixels))
	if st.opening > 0 {
		_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f%%)\n", "Unmapped area (opened)", unmappedOpenedHa, pct(st.unmappedOpenedPx, st.boundaryPixels))
	} else {
		_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f%%, opening disabled)\n", "Unmapped area (opened)", unmappedOpenedHa, pct(st.unmappedOpenedPx, st.boundaryPixels))
	}
	_, _ = fmt.Fprintf(out, "  %-26s : %d (>= %.2f ha)\n", "Qualifying gaps found", len(st.allResults), st.minHa)
	if cfg.maxFeatures > 0 && st.exportedGaps < len(st.allResults) {
		_, _ = fmt.Fprintf(out, "  %-26s : %d (limited by -max-out)\n", "Exported gaps", st.exportedGaps)
	} else {
		_, _ = fmt.Fprintf(out, "  %-26s : %d\n", "Exported gaps", st.exportedGaps)
	}
	_, _ = fmt.Fprintf(out, "  %-26s : %.2f ha (%.2f%% of analysis area)\n", "Qualifying gap area", totalGapAreaHa, gapAreaPct)
	if n := len(st.allResults); n > 0 {
		largestHa := st.allResults[0].AreaHa
		smallestHa := st.allResults[n-1].AreaHa
		avgHa := totalGapAreaHa / float64(n)
		medianHa := st.allResults[n/2].AreaHa
		if n%2 == 0 {
			medianHa = (st.allResults[n/2-1].AreaHa + st.allResults[n/2].AreaHa) / 2.0
		}
		_, _ = fmt.Fprintf(out, "  %-26s : %.2f / %.2f / %.2f / %.2f ha\n",
			"Gap size (max/avg/med/min)", largestHa, avgHa, medianHa, smallestHa)
	}

	if flushLog != nil {
		flushLog()
	}

	_, _ = fmt.Fprintf(out, "\nGenerated files:\n")
	_, _ = fmt.Fprintf(out, "  %-7s : %s (%s, %d features)\n", "GeoJSON", cfg.outGeoJSON, formatFileSize(cfg.outGeoJSON), st.exportedGaps)
	_, _ = fmt.Fprintf(out, "  %-7s : %s (%s, %d rows)\n", "CSV", cfg.outCSV, formatFileSize(cfg.outCSV), st.exportedGaps)
	_, _ = fmt.Fprintf(out, "  %-7s : %s (%s, %d x %d px)\n", "PNG", cfg.outPNG, formatFileSize(cfg.outPNG), st.previewW, st.previewH)
	_, _ = fmt.Fprintf(out, "  %-7s : %s (%s)\n", "Log", cfg.outLog, formatFileSize(cfg.outLog))

	if len(st.allResults) == 0 {
		_, _ = fmt.Fprintf(out, "\nTop gaps:\n  No unmapped gaps >= %.2f ha were found.\n", st.minHa)
		return
	}
	limit := min(25, len(st.allResults))
	_, _ = fmt.Fprintf(out, "\nTop %d gaps (of %d found):\n", limit, len(st.allResults))
	for i := 0; i < limit; i++ {
		g := st.allResults[i]
		_, _ = fmt.Fprintf(out, "  #%2d  %8.2f ha  (%8.5f, %9.5f)  %s\n",
			g.Rank, g.AreaHa, g.CenterLat, g.CenterLon, export.OSMURL(g))
	}
	_, _ = fmt.Fprintf(out, "\n")
}

// rasterStage carries the populated bitset, preview canvas, projection, loss diagnostics, and boundary metadata into post-processing.
type rasterStage struct {
	bs             *raster.Bitset
	preview        *export.PreviewMap
	proj           geo.LAEA
	originX        float64
	originY        float64
	pbfTimestamp   time.Time
	boundaryDesc   string
	boundaryRings  int
	boundaryPixels int64
	seaPixels      int64
	coveredPixels  int64
	unmappedRawPx  int64
	ignoredByTag   int64
	ignoredByArea  int64
	losses         rasterLossStats
}

/*
buildRaster executes the three-pass PBF import, selects the analysis boundary, optionally intersects it with external land polygons, and rasterizes all qualifying covered areas into a bitset.
When -land-polygons is configured, the full administrative boundary is first painted blue on the preview map, clipped in-place via Bitset.And against the rasterized land mask, and the remaining land extent is overwritten in green so excluded sea areas appear blue.
Missing ways, open ring chains, missing nodes, and area-threshold skips are accumulated into rasterLossStats and recorded individually via diagLog.
Isolating ingestion and rasterization in a dedicated stack frame ensures all large intermediate way, relation, and coordinate tables become immediately eligible for garbage collection upon return.
*/
func buildRaster(ctx context.Context, cfg *config, diagLog *log.Logger) (*rasterStage, error) {
	if diagLog == nil {
		diagLog = log.New(io.Discard, "", 0)
	}

	pbfOpts := pbf.Options{
		Path:         cfg.pbfPath,
		BoundaryRel:  cfg.boundaryRel,
		BoundaryName: cfg.boundaryName,
		Workers:      cfg.workers,
		Classifier:   cfg.classifier,
	}

	// Run pass 1 to scan administrative boundary and covered multipolygon relations.
	t0 := time.Now()
	p1, err := pbf.ReadPass1(ctx, pbfOpts)
	if err != nil {
		return nil, fmt.Errorf("pass 1 (relations): %w", err)
	}
	tsDesc := "unknown"
	if !p1.HeaderTimestamp.IsZero() {
		tsDesc = p1.HeaderTimestamp.UTC().Format(time.RFC3339)
	}
	wpDesc := p1.HeaderWritingProgram
	if wpDesc == "" {
		wpDesc = "unknown"
	}
	log.Printf("PBF header: timestamp=%s, writer=%q.", tsDesc, wpDesc)
	log.Printf("Pass 1 completed (%s): %d boundary candidates, %d multipolygons (%d area-limited, %d ignored by tag), %d referenced ways.",
		time.Since(t0).Round(time.Millisecond), len(p1.BoundaryRels), len(p1.CoveredRels), p1.AreaLimitedRels, p1.IgnoredRelsCount, len(p1.NeededWays))

	centerLat, centerLon := 51.1657, 10.4515
	if p1.HeaderBounds != nil && p1.HeaderBounds.MaxLat > p1.HeaderBounds.MinLat && p1.HeaderBounds.MaxLon > p1.HeaderBounds.MinLon {
		centerLat = (p1.HeaderBounds.MinLat + p1.HeaderBounds.MaxLat) / 2.0
		centerLon = (p1.HeaderBounds.MinLon + p1.HeaderBounds.MaxLon) / 2.0
	}
	proj := geo.NewLAEA(centerLat, centerLon)

	// Run pass 2 to collect relation member ways and standalone covered surface polygons.
	t0 = time.Now()
	p2, err := pbf.ReadPass2(ctx, pbfOpts, p1.NeededWays)
	if err != nil {
		return nil, fmt.Errorf("pass 2 (ways): %w", err)
	}
	losses := rasterLossStats{
		neededNodesSorted: slices.IsSorted(p2.NeededNodes),
	}
	if !losses.neededNodesSorted {
		log.Printf("WARNING: neededNodes slice is not sorted after Pass 2; binary searches in Pass 3 will fail.")
	}
	log.Printf("Pass 2 completed (%s): %d relation ways, %d standalone polygons (%d area-limited, %d ignored by tag), %d unique nodes (sorted=%t).",
		time.Since(t0).Round(time.Millisecond), len(p2.RelWays), len(p2.StandaloneWays)+len(p2.ConditionalWays), len(p2.ConditionalWays), p2.IgnoredWaysCount, len(p2.NeededNodes), losses.neededNodesSorted)

	// Run pass 3 to load and project all required node coordinates.
	t0 = time.Now()
	coords, err := pbf.ReadPass3(ctx, pbfOpts, p2.NeededNodes, proj)
	if err != nil {
		return nil, fmt.Errorf("pass 3 (nodes): %w", err)
	}
	for _, c := range coords {
		if math.IsNaN(float64(c.X)) {
			losses.missingCoordsPass3++
		}
	}
	log.Printf("Pass 3 completed (%s): loaded coordinates (~%.1f MB, %d missing nodes).",
		time.Since(t0).Round(time.Millisecond), float64(len(p2.NeededNodes)*16)/(1024*1024), losses.missingCoordsPass3)

	// Select the active analysis boundary and allocate the equal-area raster bitset.
	boundaryRings, boundaryDesc := boundary.Select(
		boundary.Options{BoundaryRel: cfg.boundaryRel, BoundaryName: cfg.boundaryName},
		p1.HeaderBounds, proj, p1.BoundaryRels, p2.RelWays, p2.NeededNodes, coords,
	)
	boundaryRingsCount := len(boundaryRings)
	log.Printf("Selected analysis boundary: %s (%d rings).", boundaryDesc, boundaryRingsCount)

	minX, minY, maxX, maxY := geo.RingsBBox(boundaryRings)
	pad := cfg.opening + 4
	originX := minX - float64(pad)*cfg.resMeters
	originY := minY - float64(pad)*cfg.resMeters
	gridW := int(math.Ceil((maxX-minX)/cfg.resMeters)) + 2*pad
	gridH := int(math.Ceil((maxY-minY)/cfg.resMeters)) + 2*pad
	invRes := 1.0 / cfg.resMeters

	bs := raster.NewBitset(gridW, gridH)
	log.Printf("Allocated raster grid: %d x %d pixels (%g m/px, bitset %.1f MB).",
		gridW, gridH, cfg.resMeters, float64(len(bs.Data)*8)/(1024*1024))

	var xsBuf []float64
	var skippedByArea int64
	t0 = time.Now()

	// Burn the active analysis boundary into the bitset as unmapped candidate cells.
	xsBuf = raster.RasterizeRings(bs, boundaryRings, originX, originY, invRes, true, xsBuf)
	rawBoundaryPixels := bs.Count()

	preview := export.NewPreviewMap(gridW, gridH)
	var seaPixels int64
	if cfg.landPolygonsPath != "" {
		// Paint the full administrative boundary blue first; land cells are painted green after clipping.
		preview.PaintBitset(bs, color.RGBA{38, 86, 138, 255})

		tLand := time.Now()
		queryBBox := landmask.QueryBBoxFromLAEA(minX, minY, maxX, maxY, proj)
		landBS := raster.NewBitset(gridW, gridH)
		landStats, lerr := landmask.Rasterize(cfg.landPolygonsPath, queryBBox, proj, landBS, originX, originY, invRes)
		if lerr != nil {
			return nil, fmt.Errorf("apply land polygons: %w", lerr)
		}
		bs.And(landBS)

		seaPixels = rawBoundaryPixels - bs.Count()
		seaHa := float64(seaPixels) * (cfg.resMeters * cfg.resMeters) / 10000.0
		log.Printf("Applied land polygon mask (%s): %d/%d polygons matched (%d rings), excluded %.2f ha sea.",
			time.Since(tLand).Round(time.Millisecond), landStats.MatchedRecords, landStats.TotalRecords, landStats.RasterizedRings, seaHa)
		if landStats.MatchedRecords == 0 {
			log.Printf("WARNING: land polygon source matched 0 polygons inside analysis boundary; verify file coverage and EPSG:4326 projection.")
		}
	}

	boundaryPixels := bs.Count()
	preview.PaintBitset(bs, color.RGBA{45, 92, 62, 255})

	// Clear all unconditional standalone covered surface polygons from the bitset.
	for _, way := range p2.StandaloneWays {
		rings, missingNodes := raster.ResolveRingsMeters([][]int64{way}, p2.NeededNodes, coords)
		if missingNodes > 0 || len(rings) == 0 {
			losses.standaloneMissingNodes += int64(missingNodes)
			losses.standaloneDropped++
			var firstNode int64
			if len(way) > 0 {
				firstNode = way[0]
			}
			diagLog.Printf("[DIAG] drop standalone_way first_node=%d nodes=%d missing_nodes=%d",
				firstNode, len(way), missingNodes)
			continue
		}
		xsBuf = raster.RasterizeRings(bs, rings, originX, originY, invRes, false, xsBuf)
	}

	// Clear area-limited standalone polygons only when their projected LAEA area does not exceed the rule threshold.
	for _, cw := range p2.ConditionalWays {
		rings, missingNodes := raster.ResolveRingsMeters([][]int64{cw.Nodes}, p2.NeededNodes, coords)
		if missingNodes > 0 || len(rings) == 0 {
			losses.condMissingNodes += int64(missingNodes)
			losses.condDropped++
			var firstNode int64
			if len(cw.Nodes) > 0 {
				firstNode = cw.Nodes[0]
			}
			diagLog.Printf("[DIAG] drop conditional_way first_node=%d nodes=%d max_ha=%g missing_nodes=%d",
				firstNode, len(cw.Nodes), cw.MaxAreaHa, missingNodes)
			continue
		}
		areaHa := geo.NetAreaHa(rings, nil)
		if areaHa > cw.MaxAreaHa {
			skippedByArea++
			diagLog.Printf("[DIAG] skip_area conditional_way first_node=%d area_ha=%.2f limit_ha=%g",
				cw.Nodes[0], areaHa, cw.MaxAreaHa)
			continue
		}
		xsBuf = raster.RasterizeRings(bs, rings, originX, originY, invRes, false, xsBuf)
	}

	// Clear all covered multipolygon relations from the bitset, applying net LAEA area checks where configured.
	for i := range p1.CoveredRels {
		rel := &p1.CoveredRels[i]
		var outerWays [][]int64
		missingOuterWays := 0
		for _, wid := range rel.WayIDs {
			if w, ok := p2.RelWays[wid]; ok {
				outerWays = append(outerWays, w)
			} else {
				missingOuterWays++
			}
		}
		outerAssembled, incOuter := raster.AssembleRings(outerWays)
		outerRings, missingOuterNodes := raster.ResolveRingsMeters(outerAssembled, p2.NeededNodes, coords)

		var innerRings [][]geo.Pt64
		missingInnerWays := 0
		incInner := 0
		missingInnerNodes := 0
		if len(rel.InnerWayIDs) > 0 {
			var innerWays [][]int64
			for _, wid := range rel.InnerWayIDs {
				if w, ok := p2.RelWays[wid]; ok {
					innerWays = append(innerWays, w)
				} else {
					missingInnerWays++
				}
			}
			var innerAssembled [][]int64
			innerAssembled, incInner = raster.AssembleRings(innerWays)
			innerRings, missingInnerNodes = raster.ResolveRingsMeters(innerAssembled, p2.NeededNodes, coords)
		}

		losses.relMissingOuterWays += int64(missingOuterWays)
		losses.relMissingInnerWays += int64(missingInnerWays)
		losses.relIncompleteOuter += int64(incOuter)
		losses.relIncompleteInner += int64(incInner)
		losses.relMissingOuterNodes += int64(missingOuterNodes)
		losses.relMissingInnerNodes += int64(missingInnerNodes)

		hasLoss := missingOuterWays > 0 || missingInnerWays > 0 ||
			incOuter > 0 || incInner > 0 ||
			missingOuterNodes > 0 || missingInnerNodes > 0

		if len(outerRings) == 0 {
			losses.relDroppedNoOuter++
			diagLog.Printf("[DIAG] drop relation id=%d reason=no_outer_rings outer_ways=%d missing_outer_ways=%d incomplete_outer=%d missing_outer_nodes=%d inner_ways=%d missing_inner_ways=%d incomplete_inner=%d missing_inner_nodes=%d",
				rel.ID, len(rel.WayIDs), missingOuterWays, incOuter, missingOuterNodes,
				len(rel.InnerWayIDs), missingInnerWays, incInner, missingInnerNodes)
			continue
		}

		if hasLoss {
			losses.relPartialLoss++
			diagLog.Printf("[DIAG] partial relation id=%d outer_rings=%d outer_ways=%d missing_outer_ways=%d incomplete_outer=%d missing_outer_nodes=%d inner_rings=%d inner_ways=%d missing_inner_ways=%d incomplete_inner=%d missing_inner_nodes=%d",
				rel.ID, len(outerRings), len(rel.WayIDs), missingOuterWays, incOuter, missingOuterNodes,
				len(innerRings), len(rel.InnerWayIDs), missingInnerWays, incInner, missingInnerNodes)
		}

		if rel.MaxAreaHa > 0 {
			areaHa := geo.NetAreaHa(outerRings, innerRings)
			if areaHa > rel.MaxAreaHa {
				skippedByArea++
				diagLog.Printf("[DIAG] skip_area relation id=%d area_ha=%.2f limit_ha=%g",
					rel.ID, areaHa, rel.MaxAreaHa)
				continue
			}
		}

		if len(innerRings) > 0 {
			outerRings = append(outerRings, innerRings...)
		}
		xsBuf = raster.RasterizeRings(bs, outerRings, originX, originY, invRes, false, xsBuf)
	}

	unmappedRawPx := bs.Count()
	coveredPixels := boundaryPixels - unmappedRawPx
	log.Printf("Rasterization completed (%s): skipped %d by area; losses: standalone_dropped=%d (missing_nodes=%d), cond_dropped=%d (missing_nodes=%d), rel_dropped=%d, rel_partial=%d (missing_ways=%d+%d, incomplete=%d+%d, missing_nodes=%d+%d).",
		time.Since(t0).Round(time.Millisecond), skippedByArea,
		losses.standaloneDropped, losses.standaloneMissingNodes,
		losses.condDropped, losses.condMissingNodes,
		losses.relDroppedNoOuter, losses.relPartialLoss,
		losses.relMissingOuterWays, losses.relMissingInnerWays,
		losses.relIncompleteOuter, losses.relIncompleteInner,
		losses.relMissingOuterNodes, losses.relMissingInnerNodes)

	return &rasterStage{
		bs:             bs,
		preview:        preview,
		proj:           proj,
		originX:        originX,
		originY:        originY,
		pbfTimestamp:   p1.HeaderTimestamp,
		boundaryDesc:   boundaryDesc,
		boundaryRings:  boundaryRingsCount,
		boundaryPixels: boundaryPixels,
		seaPixels:      seaPixels,
		coveredPixels:  coveredPixels,
		unmappedRawPx:  unmappedRawPx,
		ignoredByTag:   p1.IgnoredRelsCount + p2.IgnoredWaysCount,
		ignoredByArea:  skippedByArea,
		losses:         losses,
	}, nil
}

/*
run orchestrates the end-to-end area-gap detection pipeline from three-pass PBF ingestion through rasterization, opening, flood-fill, and file export.
It opens the buffered diagnostic logfile osm-coverage-gaps.log, mirrors console reports and progress logs into it, and writes per-object geometry loss diagnostics exclusively to the logfile.
All detected results are exported unconditionally as GeoJSON, CSV, PNG preview, and log files followed by final coverage statistics and the top 25 gaps.
*/
func run(ctx context.Context, cfg *config) (err error) {
	tTotal := time.Now()
	minPixels := int(math.Ceil((cfg.minHa * 10000.0) / (cfg.resMeters * cfg.resMeters)))

	logFile, err := os.Create(cfg.outLog)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	logBuf := bufio.NewWriterSize(logFile, 64*1024)
	diagLog := log.New(logBuf, "", log.LstdFlags)
	log.SetOutput(io.MultiWriter(os.Stderr, logBuf))
	defer func() {
		if err != nil {
			diagLog.Printf("Error: %v", err)
		}
		log.SetOutput(os.Stderr)
		if ferr := logBuf.Flush(); ferr != nil && err == nil {
			err = fmt.Errorf("flush log file: %w", ferr)
		}
		if cerr := logFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close log file: %w", cerr)
		}
	}()

	reportOut := io.MultiWriter(os.Stdout, logBuf)
	printSettings(reportOut, cfg, minPixels)

	stage, err := buildRaster(ctx, cfg, diagLog)
	if err != nil {
		return err
	}
	runtime.GC()

	// Apply morphological opening to suppress narrow road corridors and sliver gaps.
	unmappedOpenedPx := stage.unmappedRawPx
	if cfg.opening > 0 {
		t0 := time.Now()
		stage.bs.Open(cfg.opening, cfg.workers)
		unmappedOpenedPx = stage.bs.Count()
		log.Printf("Morphological opening (r=%d px, ~%.0f m width) completed (%s).",
			cfg.opening, float64(cfg.opening*2)*cfg.resMeters, time.Since(t0).Round(time.Millisecond))
	}

	stage.preview.PaintBitset(stage.bs, color.RGBA{165, 130, 55, 255})

	// Extract connected unmapped gap components meeting the minimum area threshold.
	colGap := color.RGBA{235, 55, 45, 255}
	onGapCells := func(cells []geo.Cell) {
		stage.preview.PaintCells(cells, colGap)
	}

	t0 := time.Now()
	allResults := gaps.Extract(stage.bs, minPixels, stage.originX, stage.originY, cfg.resMeters, stage.proj, onGapCells)
	log.Printf("Flood-fill and contour extraction completed (%s): %d gaps >= %.2f ha (%d pixels).",
		time.Since(t0).Round(time.Millisecond), len(allResults), cfg.minHa, minPixels)

	exported := allResults
	if cfg.maxFeatures > 0 && len(exported) > cfg.maxFeatures {
		exported = exported[:cfg.maxFeatures]
	}

	// Write all output files in GeoJSON, CSV, and PNG formats.
	if err := export.WriteGeoJSON(cfg.outGeoJSON, exported); err != nil {
		return fmt.Errorf("write GeoJSON: %w", err)
	}
	log.Printf("Wrote GeoJSON: %s (%d features).", cfg.outGeoJSON, len(exported))

	if err := export.WriteCSV(cfg.outCSV, exported); err != nil {
		return fmt.Errorf("write CSV: %w", err)
	}
	log.Printf("Wrote CSV: %s (%d rows).", cfg.outCSV, len(exported))

	if err := stage.preview.Save(cfg.outPNG); err != nil {
		return fmt.Errorf("write PNG: %w", err)
	}
	log.Printf("Wrote preview PNG: %s (%d x %d px).", cfg.outPNG, stage.preview.W, stage.preview.H)

	printSummary(reportOut, cfg, &summaryStats{
		runtime:          time.Since(tTotal).Round(time.Millisecond),
		pbfTimestamp:     stage.pbfTimestamp,
		boundaryDesc:     stage.boundaryDesc,
		boundaryRings:    stage.boundaryRings,
		gridW:            stage.bs.W,
		gridH:            stage.bs.H,
		resMeters:        cfg.resMeters,
		minHa:            cfg.minHa,
		opening:          cfg.opening,
		boundaryPixels:   stage.boundaryPixels,
		seaPixels:        stage.seaPixels,
		coveredPixels:    stage.coveredPixels,
		unmappedRawPx:    stage.unmappedRawPx,
		unmappedOpenedPx: unmappedOpenedPx,
		ignoredByTag:     stage.ignoredByTag,
		ignoredByArea:    stage.ignoredByArea,
		losses:           stage.losses,
		exportedGaps:     len(exported),
		previewW:         stage.preview.W,
		previewH:         stage.preview.H,
		allResults:       allResults,
	}, func() {
		_ = logBuf.Flush()
	})
	return nil
}

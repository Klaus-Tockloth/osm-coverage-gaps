package pbf

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/osmtags"
)

// Options configures the three-pass PBF scanner.
type Options struct {
	Path         string
	BoundaryRel  int64
	BoundaryName string
	Workers      int
	Classifier   *osmtags.Classifier
}

// RelInfo stores metadata and referenced way IDs extracted from an OSM relation.
// For boundary relations, WayIDs contains all outer and inner member way IDs.
// For covered multipolygon relations, WayIDs stores outer/unassigned member ways and InnerWayIDs stores inner holes.
type RelInfo struct {
	ID          int64
	Name        string
	AdminLevel  int
	IsBoundary  bool
	WayIDs      []int64
	InnerWayIDs []int64
	MaxAreaHa   float64
}

// Pass1Result aggregates the relation scan outputs, PBF header metadata, and diagnostics from Pass 1.
type Pass1Result struct {
	HeaderBounds         *osm.Bounds
	HeaderTimestamp      time.Time
	HeaderWritingProgram string
	BoundaryRels         []RelInfo
	CoveredRels          []RelInfo
	NeededWays           map[int64]struct{}
	AreaLimitedRels      int
	IgnoredRelsCount     int64
}

// ConditionalWay stores a closed standalone covered way subject to a maximum LAEA polygon area threshold.
type ConditionalWay struct {
	Nodes     []int64
	MaxAreaHa float64
}

// Pass2Result aggregates member ways, standalone covered polygons, required nodes, and diagnostics from Pass 2.
type Pass2Result struct {
	RelWays          map[int64][]int64
	StandaloneWays   [][]int64
	ConditionalWays  []ConditionalWay
	NeededNodes      []int64
	IgnoredWaysCount int64
}

/*
ReadPass1 scans only relations from the PBF file to identify candidate administrative boundaries and covered multipolygon areas.
It configures the concurrent osmpbf scanner to skip nodes and ways while filtering relations by explicit boundary parameters, admin levels 2, 4, or 6, and the configured surface Classifier.
Outer and inner member ways of covered multipolygons are separated so downstream rasterization can compute exact net LAEA areas for area-limited exclusion rules.
*/
func ReadPass1(ctx context.Context, opts Options) (*Pass1Result, error) {
	classifier := opts.Classifier
	if classifier == nil {
		classifier = osmtags.DefaultClassifier()
	}

	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var ignoredRels atomic.Int64

	sc := osmpbf.New(ctx, f, opts.Workers)
	sc.SkipNodes = true
	sc.SkipWays = true
	sc.FilterRelation = func(r *osm.Relation) bool {
		if opts.BoundaryRel > 0 && int64(r.ID) == opts.BoundaryRel {
			return true
		}
		if r.Tags.Find("type") == "multipolygon" {
			dec := classifier.Classify(r.Tags)
			if dec.Mode != osmtags.CoverNone {
				return true
			}
			if dec.IgnoredByRule {
				ignoredRels.Add(1)
			}
		}
		if r.Tags.Find("boundary") == "administrative" {
			if opts.BoundaryName != "" {
				return strings.EqualFold(r.Tags.Find("name"), opts.BoundaryName)
			}
			al := r.Tags.Find("admin_level")
			return al == "2" || al == "4" || al == "6"
		}
		return false
	}
	defer func() { _ = sc.Close() }()

	hdr, err := sc.Header()
	if err != nil {
		return nil, fmt.Errorf("read PBF header: %w", err)
	}

	var boundaryRels []RelInfo
	var coveredRels []RelInfo
	neededWays := make(map[int64]struct{}, 65536)
	areaLimitedRels := 0

	for sc.Scan() {
		r, ok := sc.Object().(*osm.Relation)
		if !ok {
			continue
		}

		isExplicitBoundary := opts.BoundaryRel > 0 && int64(r.ID) == opts.BoundaryRel
		isAdminBoundary := r.Tags.Find("boundary") == "administrative" &&
			(opts.BoundaryName == "" || strings.EqualFold(r.Tags.Find("name"), opts.BoundaryName))
		if isExplicitBoundary || (opts.BoundaryRel == 0 && isAdminBoundary) {
			al, _ := strconv.Atoi(r.Tags.Find("admin_level"))
			var ways []int64
			for _, m := range r.Members {
				if m.Type != osm.TypeWay {
					continue
				}
				switch m.Role {
				case "", "outer", "inner", "enclave", "exclave":
					ways = append(ways, m.Ref)
					neededWays[m.Ref] = struct{}{}
				}
			}
			if len(ways) > 0 {
				boundaryRels = append(boundaryRels, RelInfo{
					ID:         int64(r.ID),
					Name:       r.Tags.Find("name"),
					AdminLevel: al,
					IsBoundary: true,
					WayIDs:     ways,
				})
			}
		}

		if r.Tags.Find("type") == "multipolygon" {
			dec := classifier.Classify(r.Tags)
			if dec.Mode == osmtags.CoverNone {
				continue
			}
			var outerWays []int64
			var innerWays []int64
			for _, m := range r.Members {
				if m.Type != osm.TypeWay {
					continue
				}
				switch m.Role {
				case "", "outer":
					outerWays = append(outerWays, m.Ref)
					neededWays[m.Ref] = struct{}{}
				case "inner":
					innerWays = append(innerWays, m.Ref)
					neededWays[m.Ref] = struct{}{}
				}
			}
			if len(outerWays) > 0 {
				if dec.Mode == osmtags.CoverMaxHa {
					areaLimitedRels++
				}
				coveredRels = append(coveredRels, RelInfo{
					ID:          int64(r.ID),
					WayIDs:      outerWays,
					InnerWayIDs: innerWays,
					MaxAreaHa:   dec.MaxAreaHa,
				})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &Pass1Result{
		HeaderBounds:         hdr.Bounds,
		HeaderTimestamp:      hdr.ReplicationTimestamp,
		HeaderWritingProgram: hdr.WritingProgram,
		BoundaryRels:         boundaryRels,
		CoveredRels:          coveredRels,
		NeededWays:           neededWays,
		AreaLimitedRels:      areaLimitedRels,
		IgnoredRelsCount:     ignoredRels.Load(),
	}, nil
}

/*
ReadPass2 streams only ways from the PBF file to collect member ways referenced by relations and standalone closed covered-area ways.
Each matching way contributes its node IDs to a deduplicated list that is periodically sorted and compacted using a dynamically advancing threshold to bound peak memory without degrading on large extracts.
Unconditional standalone ways are kept in a compact [][]int64 slice while area-limited standalone ways are stored separately in ConditionalWays.
*/
func ReadPass2(ctx context.Context, opts Options, neededWays map[int64]struct{}) (*Pass2Result, error) {
	classifier := opts.Classifier
	if classifier == nil {
		classifier = osmtags.DefaultClassifier()
	}

	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var ignoredWays atomic.Int64

	sc := osmpbf.New(ctx, f, opts.Workers)
	sc.SkipNodes = true
	sc.SkipRelations = true
	sc.FilterWay = func(w *osm.Way) bool {
		if _, ok := neededWays[int64(w.ID)]; ok {
			return true
		}
		if len(w.Nodes) >= 4 && w.Nodes[0].ID == w.Nodes[len(w.Nodes)-1].ID {
			dec := classifier.Classify(w.Tags)
			if dec.Mode != osmtags.CoverNone {
				return true
			}
			if dec.IgnoredByRule {
				ignoredWays.Add(1)
			}
		}
		return false
	}
	defer func() { _ = sc.Close() }()

	const (
		initialCompactThreshold = 80_000_000
		minCompactBatch         = 40_000_000
	)

	relWays := make(map[int64][]int64, len(neededWays))
	var standaloneWays [][]int64
	var conditionalWays []ConditionalWay
	var neededNodes []int64
	nextCompact := initialCompactThreshold

	compactNodes := func() {
		slices.Sort(neededNodes)
		neededNodes = slices.Compact(neededNodes)
	}

	for sc.Scan() {
		w, ok := sc.Object().(*osm.Way)
		if !ok || len(w.Nodes) < 2 {
			continue
		}
		nids := make([]int64, len(w.Nodes))
		for i, wn := range w.Nodes {
			nids[i] = int64(wn.ID)
		}
		_, need := neededWays[int64(w.ID)]
		if need {
			relWays[int64(w.ID)] = nids
		}
		if len(nids) >= 4 && nids[0] == nids[len(nids)-1] {
			dec := classifier.Classify(w.Tags)
			switch dec.Mode {
			case osmtags.CoverAlways:
				standaloneWays = append(standaloneWays, nids)
			case osmtags.CoverMaxHa:
				conditionalWays = append(conditionalWays, ConditionalWay{
					Nodes:     nids,
					MaxAreaHa: dec.MaxAreaHa,
				})
			case osmtags.CoverNone:
				if need && dec.IgnoredByRule {
					ignoredWays.Add(1)
				}
			}
		}
		neededNodes = append(neededNodes, nids...)
		if len(neededNodes) > nextCompact {
			compactNodes()
			batch := max(minCompactBatch, len(neededNodes)/4)
			nextCompact = max(initialCompactThreshold, len(neededNodes)+batch)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	compactNodes()
	return &Pass2Result{
		RelWays:          relWays,
		StandaloneWays:   standaloneWays,
		ConditionalWays:  conditionalWays,
		NeededNodes:      neededNodes,
		IgnoredWaysCount: ignoredWays.Load(),
	}, nil
}

/*
ReadPass3 scans only nodes from the PBF file and projects every required node into planar LAEA meters stored as compact 32-bit coordinates.
The output coordinate slice is aligned positionally with the sorted neededNodes slice and pre-filled with NaN sentinels so missing nodes remain detectable.
The concurrent scanner filter uses lock-free binary search on neededNodes to discard unneeded nodes early during decoding.
Matching nodes are transformed via Forward and stored directly into their indexed coordinate slot.
*/
func ReadPass3(ctx context.Context, opts Options, neededNodes []int64, proj geo.LAEA) ([]geo.Pt32, error) {
	coords := make([]geo.Pt32, len(neededNodes))
	nan32 := float32(math.NaN())
	for i := range coords {
		coords[i].X = nan32
	}

	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	sc := osmpbf.New(ctx, f, opts.Workers)
	sc.SkipWays = true
	sc.SkipRelations = true
	sc.FilterNode = func(n *osm.Node) bool {
		_, ok := slices.BinarySearch(neededNodes, int64(n.ID))
		return ok
	}
	defer func() { _ = sc.Close() }()

	for sc.Scan() {
		n, ok := sc.Object().(*osm.Node)
		if !ok {
			continue
		}
		if idx, found := slices.BinarySearch(neededNodes, int64(n.ID)); found {
			x, y := proj.Forward(n.Lat, n.Lon)
			coords[idx] = geo.Pt32{X: float32(x), Y: float32(y)}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return coords, nil
}

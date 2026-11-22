package boundary

import (
	"math"
	"strings"
	"testing"

	"github.com/paulmach/osm"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/pbf"
)

func boundaryTestFixture() (geo.LAEA, map[int64][]int64, []int64, []geo.Pt32) {
	proj := geo.NewLAEA(51.0, 10.0)
	neededNodes := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9}
	coords := []geo.Pt32{
		{X: 0, Y: 0},
		{X: 100, Y: 0},
		{X: 100, Y: 100},
		{X: 0, Y: 100},
		{X: 0, Y: 0},
		{X: 500, Y: 0},
		{X: 500, Y: 500},
		{X: 0, Y: 500},
		{X: float32(math.NaN()), Y: 0},
	}
	relWays := map[int64][]int64{
		101: {1, 2, 3, 4, 1}, // closed 100x100 m
		102: {5, 6, 7, 8, 5}, // closed 500x500 m
		103: {1, 2},          // open unclosed way (inc=1)
		104: {1, 2, 9, 1},    // closed way containing NaN node (missingNodes=1)
	}
	return proj, relWays, neededNodes, coords
}

func TestSelectExplicitCompleteAndPartial(t *testing.T) {
	proj, relWays, neededNodes, coords := boundaryTestFixture()

	relComplete := pbf.RelInfo{ID: 10, Name: "Small", AdminLevel: 6, WayIDs: []int64{101}}
	relPartialLarge := pbf.RelInfo{ID: 20, Name: "Large", AdminLevel: 4, WayIDs: []int64{102, 999}}
	relPartialMultiLoss := pbf.RelInfo{ID: 21, Name: "MultiLoss", AdminLevel: 6, WayIDs: []int64{101, 999, 103, 104}}

	// Explicit selection prefers complete candidates over larger partial candidates.
	rings, label := Select(
		Options{BoundaryRel: 10}, nil, proj,
		[]pbf.RelInfo{relPartialLarge, relComplete}, relWays, neededNodes, coords,
	)
	if len(rings) != 1 || label != "Relation 10 (Small, admin_level=6)" {
		t.Fatalf("explicit complete got rings=%d label=%q", len(rings), label)
	}

	// Explicit selection falls back to largest partial candidate with warning label.
	rings, label = Select(
		Options{BoundaryName: "Large"}, nil, proj,
		[]pbf.RelInfo{relPartialMultiLoss, relPartialLarge}, relWays, neededNodes, coords,
	)
	if len(rings) != 1 || label != "Relation 20 (Large, admin_level=4) [WARNING: 1 incomplete parts]" {
		t.Fatalf("explicit partial got rings=%d label=%q", len(rings), label)
	}

	// Verify incomplete counter aggregates missingWays + open chains + missingNodes (1+1+1=3).
	rings, label = Select(
		Options{BoundaryRel: 21}, nil, proj,
		[]pbf.RelInfo{relPartialMultiLoss}, relWays, neededNodes, coords,
	)
	if len(rings) != 1 || !strings.Contains(label, "[WARNING: 3 incomplete parts]") {
		t.Fatalf("expected 3 incomplete parts warning, got rings=%d label=%q", len(rings), label)
	}
}

func TestSelectAutoAdminLevelAndArea(t *testing.T) {
	proj, relWays, neededNodes, coords := boundaryTestFixture()

	rels := []pbf.RelInfo{
		{ID: 1, Name: "BrokenCountry", AdminLevel: 2, WayIDs: []int64{102, 999}}, // partial -> ignored in auto
		{ID: 4, Name: "District", AdminLevel: 6, WayIDs: []int64{102}},
		{ID: 2, Name: "SmallState", AdminLevel: 4, WayIDs: []int64{101}},
		{ID: 3, Name: "LargeState", AdminLevel: 4, WayIDs: []int64{102}},
	}

	rings, label := Select(Options{}, nil, proj, rels, relWays, neededNodes, coords)
	if len(rings) != 1 || label != "Auto: Relation 3 (LargeState, admin_level=4)" {
		t.Fatalf("auto selection got rings=%d label=%q", len(rings), label)
	}
}

func TestSelectFallbacks(t *testing.T) {
	proj, relWays, neededNodes, coords := boundaryTestFixture()
	onlyPartial := []pbf.RelInfo{{ID: 1, Name: "Partial", AdminLevel: 4, WayIDs: []int64{101, 999}}}

	// Auto mode with no complete relations falls back to valid PBF header bounds.
	hdr := &osm.Bounds{MinLat: 50.0, MaxLat: 52.0, MinLon: 9.0, MaxLon: 11.0}
	rings, label := Select(Options{}, hdr, proj, onlyPartial, relWays, neededNodes, coords)
	if label != "Fallback: PBF header bounding box" {
		t.Fatalf("header fallback label = %q", label)
	}
	if len(rings) != 1 || len(rings[0]) != 5 || rings[0][0] != rings[0][4] {
		t.Fatalf("header fallback produced invalid rectangle: %+v", rings)
	}
	wantX, wantY := proj.Forward(hdr.MinLat, hdr.MinLon)
	if math.Abs(rings[0][0].X-wantX) > 1e-6 || math.Abs(rings[0][0].Y-wantY) > 1e-6 {
		t.Fatalf("header fallback origin = %+v, want (%v,%v)", rings[0][0], wantX, wantY)
	}

	// Degenerate header bounds falls back to data bounding box while skipping NaN coordinates.
	badHdr := &osm.Bounds{MinLat: 52.0, MaxLat: 50.0, MinLon: 9.0, MaxLon: 11.0}
	rings, label = Select(Options{}, badHdr, proj, nil, relWays, neededNodes, coords)
	if label != "Fallback: data bounding box" {
		t.Fatalf("data fallback label = %q", label)
	}
	if len(rings) != 1 || len(rings[0]) != 5 || rings[0][0] != rings[0][4] {
		t.Fatalf("data fallback produced invalid rectangle: %+v", rings)
	}
	minX, minY, maxX, maxY := geo.RingsBBox(rings)
	if minX != 0 || minY != 0 || maxX != 500 || maxY != 500 {
		t.Fatalf("data fallback bbox = (%v,%v,%v,%v), want (0,0,500,500)", minX, minY, maxX, maxY)
	}
}

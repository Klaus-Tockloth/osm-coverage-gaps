package gaps

import (
	"math"
	"testing"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/raster"
)

func TestSimplifyCollinear(t *testing.T) {
	// Start/end vertex (5,0) and (10,5) are collinear and must be pruned.
	ring := [][2]int{{5, 0}, {10, 0}, {10, 5}, {10, 10}, {0, 10}, {0, 0}, {5, 0}}
	got := simplifyCollinear(ring)
	if len(got) != 5 || got[0] != got[len(got)-1] {
		t.Fatalf("simplifyCollinear(square) = %v, want 5 closed vertices", got)
	}

	// Triangle (<= 4 vertices) returned unchanged.
	tri := [][2]int{{0, 0}, {2, 0}, {0, 2}, {0, 0}}
	if len(simplifyCollinear(tri)) != 4 {
		t.Fatalf("simplifyCollinear(triangle) altered 4-vertex ring")
	}

	// Degenerate all-collinear ring (< 3 corners) falls back to original.
	degen := [][2]int{{0, 0}, {1, 0}, {2, 0}, {3, 0}, {0, 0}}
	if len(simplifyCollinear(degen)) != len(degen) {
		t.Fatalf("simplifyCollinear(degenerate) should preserve original ring")
	}
}

func TestPerpDistSq(t *testing.T) {
	if got := perpDistSq([2]int{6, 8}, [2]int{3, 4}, [2]int{3, 4}); math.Abs(got-25) > 1e-9 {
		t.Fatalf("degenerate segment perpDistSq = %v, want 25", got)
	}
	if got := perpDistSq([2]int{5, 3}, [2]int{0, 0}, [2]int{10, 0}); math.Abs(got-9) > 1e-9 {
		t.Fatalf("interior perpDistSq = %v, want 9", got)
	}
	if got := perpDistSq([2]int{-4, 3}, [2]int{0, 0}, [2]int{10, 0}); math.Abs(got-25) > 1e-9 {
		t.Fatalf("clamped start perpDistSq = %v, want 25", got)
	}
	if got := perpDistSq([2]int{13, 4}, [2]int{0, 0}, [2]int{10, 0}); math.Abs(got-25) > 1e-9 {
		t.Fatalf("clamped end perpDistSq = %v, want 25", got)
	}
}

func TestSimplifyClosedRing(t *testing.T) {
	// Right triangle with a 10-step staircase hypotenuse (23 closed vertices, no collinear points).
	stair := [][2]int{{0, 0}, {10, 0}, {10, 10}}
	for k := 9; k >= 0; k-- {
		stair = append(stair, [2]int{k, k + 1}, [2]int{k, k})
	}

	// eps <= 0 keeps collinear-simplified vertices intact.
	if got := simplifyClosedRing(stair, 0); len(got) != len(stair) {
		t.Fatalf("simplifyClosedRing(eps=0) len = %d, want %d", len(got), len(stair))
	}

	// eps = 0.8 exceeds 1/sqrt(2) (~0.707), collapsing the staircase to a closed triangle (4 vertices).
	simp := simplifyClosedRing(stair, 0.8)
	if len(simp) != 4 || simp[0] != simp[len(simp)-1] {
		t.Fatalf("simplifyClosedRing(eps=0.8) = %v, want 4 closed vertices", simp)
	}

	// Excessive eps triggers the < 4 collapse fallback and preserves the pre-simplified ring.
	fallback := simplifyClosedRing(stair, 100.0)
	if len(fallback) != len(stair) {
		t.Fatalf("simplifyClosedRing(eps=100) len = %d, want fallback len %d", len(fallback), len(stair))
	}
}

func TestSignedArea2(t *testing.T) {
	ccw := [][2]int{{0, 0}, {10, 0}, {10, 5}, {0, 5}, {0, 0}}
	cw := [][2]int{{0, 0}, {0, 5}, {10, 5}, {10, 0}, {0, 0}}
	large := [][2]int{{0, 0}, {100000, 0}, {100000, 100000}, {0, 100000}, {0, 0}}

	if got := signedArea2(ccw); got != 100 {
		t.Fatalf("signedArea2(CCW) = %d, want 100", got)
	}
	if got := signedArea2(cw); got != -100 {
		t.Fatalf("signedArea2(CW) = %d, want -100", got)
	}
	if got := signedArea2(large); got != 20_000_000_000 {
		t.Fatalf("signedArea2(large) = %d, want 20000000000", got)
	}
}

func TestTraceComponentRings(t *testing.T) {
	proj := geo.NewLAEA(51.0, 10.0)

	// Single 1x1 pixel.
	single := traceComponentRings([]geo.Cell{{X: 0, Y: 0}}, 0, 0, 0, 0, 0, 0, 100, proj)
	if len(single) != 1 || len(single[0]) != 5 || single[0][0] != single[0][4] {
		t.Fatalf("single pixel rings = %+v, want 1 closed 5-vertex ring", single)
	}

	// 5x5 donut with 1x1 center hole -> 1 outer ring + 1 hole ring.
	var donut []geo.Cell
	for y := int32(0); y < 5; y++ {
		for x := int32(0); x < 5; x++ {
			if x == 2 && y == 2 {
				continue
			}
			donut = append(donut, geo.Cell{X: x, Y: y})
		}
	}
	donutRings := traceComponentRings(donut, 0, 0, 4, 4, 0, 0, 100, proj)
	if len(donutRings) != 2 {
		t.Fatalf("donut rings count = %d, want 2", len(donutRings))
	}
	for i, r := range donutRings {
		if len(r) != 5 || r[0] != r[len(r)-1] {
			t.Fatalf("donut ring %d not a closed 5-vertex ring: %+v", i, r)
		}
	}

	// Saddle-point 1: two diagonally touching interior holes inside a 6x6 component -> 1 outer + 2 holes.
	var twoHoles []geo.Cell
	for y := int32(0); y < 6; y++ {
		for x := int32(0); x < 6; x++ {
			if (x == 2 && y == 2) || (x == 3 && y == 3) {
				continue
			}
			twoHoles = append(twoHoles, geo.Cell{X: x, Y: y})
		}
	}
	saddleHoles := traceComponentRings(twoHoles, 0, 0, 5, 5, 0, 0, 100, proj)
	if len(saddleHoles) != 3 {
		t.Fatalf("diagonally touching holes rings count = %d, want 3", len(saddleHoles))
	}

	// Saddle-point 2: interior hole touching exterior corner diagonally -> 1 outer + 1 hole.
	var cornerPinch []geo.Cell
	for y := int32(0); y < 5; y++ {
		for x := int32(0); x < 5; x++ {
			if (x == 0 && y == 0) || (x == 1 && y == 1) {
				continue
			}
			cornerPinch = append(cornerPinch, geo.Cell{X: x, Y: y})
		}
	}
	pinchRings := traceComponentRings(cornerPinch, 0, 0, 4, 4, 0, 0, 100, proj)
	if len(pinchRings) != 2 {
		t.Fatalf("corner-pinch rings count = %d, want 2", len(pinchRings))
	}
}

func TestExtract(t *testing.T) {
	proj := geo.NewLAEA(51.0, 10.0)
	res := 100.0 // 1 pixel == 1.0 ha

	// Empty bitset yields no results.
	empty := raster.NewBitset(16, 16)
	if got := Extract(empty, 1, 0, 0, res, proj, nil); len(got) != 0 {
		t.Fatalf("Extract(empty) returned %d results, want 0", len(got))
	}

	bs := raster.NewBitset(80, 20)
	// Component A: 4x3 block crossing the 63|64 word boundary (12 px = 12 ha).
	for y := 2; y <= 4; y++ {
		bs.SetSpan(y, 62, 65, true)
	}
	// Component B: 3x2 block at raster origin border (6 px = 6 ha).
	for y := 0; y <= 1; y++ {
		bs.SetSpan(y, 0, 2, true)
	}
	// Diagonal pair (must NOT merge under 4-connectivity; each is 1 px < minPixels=2).
	bs.SetSpan(10, 10, 10, true)
	bs.SetSpan(11, 11, 11, true)

	callbackCalls := 0
	callbackCells := 0
	results := Extract(bs, 2, 0, 0, res, proj, func(cells []geo.Cell) {
		callbackCalls++
		callbackCells += len(cells)
	})

	if bs.Count() != 0 {
		t.Fatalf("bitset not cleared in-place by Extract: remaining=%d", bs.Count())
	}
	if len(results) != 2 {
		t.Fatalf("Extract() returned %d results, want 2", len(results))
	}
	if callbackCalls != 2 || callbackCells != 18 {
		t.Fatalf("onGapCells called %d times with %d cells, want 2 calls and 18 cells", callbackCalls, callbackCells)
	}

	if results[0].Rank != 1 || results[0].Pixels != 12 || results[0].AreaHa != 12.0 {
		t.Fatalf("results[0] = %+v, want Rank=1 Pixels=12 AreaHa=12.0", results[0])
	}
	if results[1].Rank != 2 || results[1].Pixels != 6 || results[1].AreaHa != 6.0 {
		t.Fatalf("results[1] = %+v, want Rank=2 Pixels=6 AreaHa=6.0", results[1])
	}
	for i, r := range results {
		if !(r.MinLat < r.CenterLat && r.CenterLat < r.MaxLat) || !(r.MinLon < r.CenterLon && r.CenterLon < r.MaxLon) {
			t.Fatalf("results[%d] centroid outside bbox: %+v", i, r)
		}
		if len(r.Rings) != 1 || len(r.Rings[0]) != 5 || r.Rings[0][0] != r.Rings[0][4] {
			t.Fatalf("results[%d] invalid contour rings: %+v", i, r.Rings)
		}
	}
}

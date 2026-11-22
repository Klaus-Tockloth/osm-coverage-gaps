package raster

import (
	"math"
	"testing"

	"osm-area-gaps/internal/geo"
)

func TestAssembleRings(t *testing.T) {
	ways := [][]int64{
		// Already closed ring.
		{10, 11, 12, 10},
		// Degenerate single-node way (skipped).
		{99},
		// Closed 3-node loop (< 4 nodes -> incomplete).
		{20, 21, 20},
		// Multi-segment ring with reversed middle segment: 1 -> 2 -> 3 -> 4 -> 1.
		{1, 2},
		{3, 2},
		{3, 4, 1},
		// Unclosed open chain -> incomplete.
		{100, 101},
		{101, 102},
	}

	rings, incomplete := AssembleRings(ways)
	if len(rings) != 2 {
		t.Fatalf("AssembleRings() returned %d rings, want 2", len(rings))
	}
	if incomplete != 2 {
		t.Fatalf("AssembleRings() incomplete = %d, want 2", incomplete)
	}

	seen := map[int]bool{}
	for _, r := range rings {
		if len(r) < 4 || r[0] != r[len(r)-1] {
			t.Fatalf("assembled ring is not properly closed: %v", r)
		}
		seen[len(r)] = true
	}
	if !seen[4] || !seen[5] {
		t.Fatalf("expected ring lengths 4 and 5, got %v", seen)
	}
}

func TestResolveRingsMeters(t *testing.T) {
	needed := []int64{1, 2, 3, 4, 5}
	coords := []geo.Pt32{
		{X: 0, Y: 0},
		{X: 10, Y: 0},
		{X: 10, Y: 10},
		{X: 0, Y: 10},
		{X: float32(math.NaN()), Y: 0},
	}

	rings := [][]int64{
		{1, 2, 3, 4, 1}, // valid
		{1, 2, 99, 1},   // node 99 absent from neededNodes
		{1, 5, 3, 1},    // node 5 has NaN coordinate
		{1, 2, 1},       // fewer than 4 vertices
	}

	resolved, missing := ResolveRingsMeters(rings, needed, coords)
	if missing != 2 {
		t.Fatalf("ResolveRingsMeters() missing = %d, want 2", missing)
	}
	if len(resolved) != 1 {
		t.Fatalf("ResolveRingsMeters() len = %d, want 1", len(resolved))
	}
	if len(resolved[0]) != 5 || resolved[0][2] != (geo.Pt64{X: 10, Y: 10}) {
		t.Fatalf("unexpected resolved ring: %+v", resolved[0])
	}
}

func TestRasterizeRings(t *testing.T) {
	bs := NewBitset(20, 20)
	var xsBuf []float64

	// Empty rings is a no-op.
	xsBuf = RasterizeRings(bs, nil, 0, 0, 0.1, true, xsBuf)
	if bs.Count() != 0 {
		t.Fatalf("empty RasterizeRings set %d pixels, want 0", bs.Count())
	}

	// Outer 10x10 px square with 4x4 px inner hole at 10 m/px (invRes=0.1).
	outer := []geo.Pt64{{X: 20, Y: 20}, {X: 120, Y: 20}, {X: 120, Y: 120}, {X: 20, Y: 120}, {X: 20, Y: 20}}
	inner := []geo.Pt64{{X: 50, Y: 50}, {X: 90, Y: 50}, {X: 90, Y: 90}, {X: 50, Y: 90}, {X: 50, Y: 50}}
	xsBuf = RasterizeRings(bs, [][]geo.Pt64{outer, inner}, 0, 0, 0.1, true, xsBuf)
	if got := bs.Count(); got != 84 {
		t.Fatalf("donut fill Count() = %d, want 84", got)
	}
	if !bs.Get(2, 2) || bs.Get(5, 5) || bs.Get(8, 8) || !bs.Get(9, 9) {
		t.Fatalf("unexpected pixel state after donut fill")
	}

	// Clearing outer+inner from a full grid preserves the inner hole.
	for y := 0; y < bs.H; y++ {
		bs.SetSpan(y, 0, bs.W-1, true)
	}
	xsBuf = RasterizeRings(bs, [][]geo.Pt64{outer, inner}, 0, 0, 0.1, false, xsBuf)
	if got := bs.Count(); got != 400-84 {
		t.Fatalf("donut erase Count() = %d, want %d", got, 400-84)
	}
	if bs.Get(3, 3) || !bs.Get(6, 6) {
		t.Fatalf("donut erase did not preserve hole or clear outer ring")
	}

	// Concave L-shape with horizontal edge aligned exactly on scanline center y=5.5.
	bs2 := NewBitset(20, 20)
	lShape := []geo.Pt64{
		{X: 2, Y: 2}, {X: 10, Y: 2}, {X: 10, Y: 5.5},
		{X: 6, Y: 5.5}, {X: 6, Y: 10}, {X: 2, Y: 10}, {X: 2, Y: 2},
	}
	xsBuf = RasterizeRings(bs2, [][]geo.Pt64{lShape}, 0, 0, 1.0, true, xsBuf)
	// Rows y=2..4: 8 px each (24); rows y=5..9: 4 px each (20); total 44.
	if got := bs2.Count(); got != 44 {
		t.Fatalf("L-shape Count() = %d, want 44", got)
	}

	// Out-of-bounds polygon skipped; partially overlapping polygon clipped.
	bs3 := NewBitset(20, 20)
	outside := []geo.Pt64{{X: 30, Y: 30}, {X: 40, Y: 30}, {X: 40, Y: 40}, {X: 30, Y: 40}, {X: 30, Y: 30}}
	xsBuf = RasterizeRings(bs3, [][]geo.Pt64{outside}, 0, 0, 1.0, true, xsBuf)
	if bs3.Count() != 0 {
		t.Fatalf("outside polygon set %d pixels, want 0", bs3.Count())
	}
	partial := []geo.Pt64{{X: -5, Y: -5}, {X: 5, Y: -5}, {X: 5, Y: 5}, {X: -5, Y: 5}, {X: -5, Y: -5}}
	_ = RasterizeRings(bs3, [][]geo.Pt64{partial}, 0, 0, 1.0, true, xsBuf)
	if got := bs3.Count(); got != 25 {
		t.Fatalf("clipped polygon Count() = %d, want 25", got)
	}
}

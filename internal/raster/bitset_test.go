package raster

import "testing"

func TestBitsetNewGetClearCount(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		wantWPR      int
		wantLastMask uint64
	}{
		{name: "single bit", w: 1, h: 2, wantWPR: 1, wantLastMask: 1},
		{name: "63 bits", w: 63, h: 1, wantWPR: 1, wantLastMask: (uint64(1) << 63) - 1},
		{name: "exact word", w: 64, h: 1, wantWPR: 1, wantLastMask: ^uint64(0)},
		{name: "word plus one", w: 65, h: 2, wantWPR: 2, wantLastMask: 1},
		{name: "multi word", w: 130, h: 3, wantWPR: 3, wantLastMask: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bs := NewBitset(tc.w, tc.h)
			if bs.WordsPerRow != tc.wantWPR {
				t.Fatalf("WordsPerRow = %d, want %d", bs.WordsPerRow, tc.wantWPR)
			}
			if bs.LastMask != tc.wantLastMask {
				t.Fatalf("LastMask = %#x, want %#x", bs.LastMask, tc.wantLastMask)
			}
			if bs.Count() != 0 {
				t.Fatalf("initial Count() = %d, want 0", bs.Count())
			}
		})
	}

	bs := NewBitset(130, 3)
	bs.SetSpan(1, 0, 129, true)
	for _, x := range []int{0, 63, 64, 127, 129} {
		if !bs.Get(x, 1) {
			t.Fatalf("expected bit (%d,1) to be set", x)
		}
		bs.Clear(x, 1)
		if bs.Get(x, 1) {
			t.Fatalf("expected bit (%d,1) to be cleared", x)
		}
	}
	if got := bs.Count(); got != 125 {
		t.Fatalf("Count() = %d, want 125", got)
	}
}

func TestBitsetSetSpan(t *testing.T) {
	bs := NewBitset(130, 4)

	// Out-of-bounds and inverted spans are no-ops.
	bs.SetSpan(-1, 0, 10, true)
	bs.SetSpan(4, 0, 10, true)
	bs.SetSpan(0, 20, 10, true)
	bs.SetSpan(0, -10, -1, true)
	bs.SetSpan(0, 130, 150, true)
	if got := bs.Count(); got != 0 {
		t.Fatalf("Count() after out-of-bounds spans = %d, want 0", got)
	}

	// Single-word set and partial clear.
	bs.SetSpan(0, 5, 10, true)
	bs.SetSpan(0, 7, 8, false)
	if bs.Get(4, 0) || !bs.Get(5, 0) || !bs.Get(6, 0) || bs.Get(7, 0) || bs.Get(8, 0) || !bs.Get(9, 0) || !bs.Get(10, 0) || bs.Get(11, 0) {
		t.Fatalf("unexpected bits on row 0 after single-word SetSpan")
	}

	// Clipped multi-word fill and multi-word clear; trailing padding bits must remain zero.
	bs.SetSpan(1, -5, 200, true)
	if pad := bs.Data[1*bs.WordsPerRow+bs.WordsPerRow-1] & ^bs.LastMask; pad != 0 {
		t.Fatalf("non-zero trailing padding bits: %#x", pad)
	}
	bs.SetSpan(1, 10, 128, false)
	if !bs.Get(9, 1) || bs.Get(10, 1) || bs.Get(64, 1) || bs.Get(128, 1) || !bs.Get(129, 1) {
		t.Fatalf("unexpected bits on row 1 after multi-word SetSpan")
	}

	// Row 0: 4 bits; Row 1: [0..9] + [129] = 11 bits; total 15.
	if got := bs.Count(); got != 15 {
		t.Fatalf("Count() = %d, want 15", got)
	}
}

func TestBitsetAnd(t *testing.T) {
	a := NewBitset(130, 4)
	b := NewBitset(130, 4)

	a.SetSpan(0, 0, 129, true)
	a.SetSpan(2, 10, 80, true)

	b.SetSpan(0, 60, 100, true)
	b.SetSpan(2, 50, 120, true)
	b.SetSpan(3, 0, 129, true)

	a.And(b)

	// Row 0: [60..100] => 41 bits; Row 2: [50..80] => 31 bits; total 72.
	if got := a.Count(); got != 72 {
		t.Fatalf("Count() after And = %d, want 72", got)
	}
	if a.Get(59, 0) || !a.Get(60, 0) || !a.Get(100, 0) || a.Get(101, 0) {
		t.Fatalf("unexpected row 0 bits after And")
	}
	if a.Get(49, 2) || !a.Get(50, 2) || !a.Get(80, 2) || a.Get(81, 2) {
		t.Fatalf("unexpected row 2 bits after And")
	}
	if a.Get(10, 3) {
		t.Fatalf("row 3 should remain cleared after And")
	}

	// Mismatched dimensions must leave receiver unchanged.
	other := NewBitset(64, 4)
	a.And(other)
	if got := a.Count(); got != 72 {
		t.Fatalf("Count() after mismatched And = %d, want 72", got)
	}
}

func TestBitsetOpen(t *testing.T) {
	// No-op guards: radius <= 0 or grid smaller than 3x3.
	small := NewBitset(2, 2)
	small.SetSpan(0, 0, 1, true)
	small.Open(1, 2)
	if small.Count() != 2 {
		t.Fatalf("Open on 2x2 modified bitset: count=%d, want 2", small.Count())
	}

	noop := NewBitset(10, 10)
	noop.SetSpan(5, 3, 6, true)
	noop.Open(0, 2)
	noop.Open(-1, 2)
	if noop.Count() != 4 {
		t.Fatalf("Open(<=0) modified bitset: count=%d, want 4", noop.Count())
	}

	// Verify LastMask prevents padding-bit leakage during horizontal dilation.
	edgeSrc := NewBitset(65, 3)
	edgeDst := NewBitset(65, 3)
	edgeSrc.SetSpan(1, 64, 64, true)
	morphStepH(edgeSrc, edgeDst, true, 1)
	if pad := edgeDst.Data[1*edgeDst.WordsPerRow+1] & ^edgeDst.LastMask; pad != 0 {
		t.Fatalf("morphStepH leaked padding bits: %#x", pad)
	}
	if !edgeDst.Get(63, 1) || !edgeDst.Get(64, 1) {
		t.Fatalf("morphStepH dilation did not set expected right-edge bits")
	}

	// Compare opening across worker counts (including workers > H).
	buildScene := func() *Bitset {
		bs := NewBitset(130, 12)
		// 5x5 block crossing the 63|64 word boundary: survives radius=1 intact (25 px).
		for y := 2; y <= 6; y++ {
			bs.SetSpan(y, 62, 66, true)
		}
		// 2-pixel-tall horizontal corridor: eliminated by radius=1.
		bs.SetSpan(9, 10, 50, true)
		bs.SetSpan(10, 10, 50, true)
		// 2-pixel-wide vertical corridor: eliminated by radius=1.
		for y := 1; y <= 7; y++ {
			bs.SetSpan(y, 20, 21, true)
		}
		// Bottom border strip: eliminated by vertical erosion.
		bs.SetSpan(0, 10, 30, true)
		return bs
	}

	var ref []uint64
	for _, workers := range []int{1, 4, 32} {
		bs := buildScene()
		bs.Open(1, workers)
		if got := bs.Count(); got != 25 {
			t.Fatalf("Open(r=1, workers=%d) Count() = %d, want 25", workers, got)
		}
		for y := 2; y <= 6; y++ {
			for x := 62; x <= 66; x++ {
				if !bs.Get(x, y) {
					t.Fatalf("Open(workers=%d) missing expected block pixel (%d,%d)", workers, x, y)
				}
			}
		}
		if ref == nil {
			ref = append([]uint64(nil), bs.Data...)
		} else {
			for i := range ref {
				if bs.Data[i] != ref[i] {
					t.Fatalf("Open(workers=%d) differed from workers=1 at word %d", workers, i)
				}
			}
		}
	}
}

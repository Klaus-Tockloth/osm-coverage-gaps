package raster

import (
	"math/bits"
	"sync"
)

// Bitset stores a two-dimensional binary raster grid packed into 64-bit words.
type Bitset struct {
	W, H        int
	WordsPerRow int
	LastMask    uint64
	Data        []uint64
}

/*
NewBitset allocates a two-dimensional bit-packed raster grid with the specified pixel width and height.
Each row is aligned to 64-bit machine words, and a trailing bitmask is precomputed to zero unused padding bits in the final word of every row.
The compact 1-bit-per-pixel representation allows nationwide analyses to remain resident in RAM.
*/
func NewBitset(w, h int) *Bitset {
	wpr := (w + 63) / 64
	lastMask := ^uint64(0)
	if rem := w % 64; rem != 0 {
		lastMask = (uint64(1) << uint(rem)) - 1
	}
	return &Bitset{
		W:           w,
		H:           h,
		WordsPerRow: wpr,
		LastMask:    lastMask,
		Data:        make([]uint64, h*wpr),
	}
}

/*
Get tests whether the pixel at column x and row y is currently set in the bitset.
It locates the backing 64-bit word via integer shift and tests the target bit with a single bitmask operation.
Callers must ensure that both coordinates lie within the allocated grid bounds.
*/
func (b *Bitset) Get(x, y int) bool {
	return (b.Data[y*b.WordsPerRow+(x>>6)] & (uint64(1) << uint(x&63))) != 0
}

/*
Clear resets the single bit at column x and row y to zero.
It performs an in-place bit-clear operation on the corresponding 64-bit word without affecting adjacent pixels.
Flood-fill extraction invokes this method to mark visited gap pixels directly inside the raster grid.
*/
func (b *Bitset) Clear(x, y int) {
	b.Data[y*b.WordsPerRow+(x>>6)] &^= uint64(1) << uint(x&63)
}

/*
And intersects this bitset in-place with other using word-wise bitwise AND.
Both bitsets must share identical grid dimensions; mismatched dimensions leave the receiver unchanged.
This operation clips the administrative analysis boundary to external land-polygon masks in linear word time.
*/
func (b *Bitset) And(other *Bitset) {
	if other == nil || b.W != other.W || b.H != other.H || len(b.Data) != len(other.Data) {
		return
	}
	for i := range b.Data {
		b.Data[i] &= other.Data[i]
	}
}

/*
Count returns the total number of set pixels across the entire bitset.
It iterates over all backing 64-bit words and accumulates their population counts using hardware-accelerated bit counting.
Because trailing padding bits in every row remain zeroed by SetSpan and morphological passes, the returned value reflects exact grid pixels.
*/
func (b *Bitset) Count() int64 {
	var total int64
	for _, w := range b.Data {
		total += int64(bits.OnesCount64(w))
	}
	return total
}

/*
SetSpan sets or clears all pixels along horizontal row y from column x0 through x1 inclusive.
Coordinates are clipped to the raster dimensions so out-of-bounds polygons can be rasterized safely without extra boundary checks.
Partial leading and trailing words are updated via bitmasks while full interior words are filled directly with all-ones or zero words.
This word-level span operation provides high throughput during scanline polygon rasterization.
*/
func (b *Bitset) SetSpan(y, x0, x1 int, val bool) {
	if y < 0 || y >= b.H {
		return
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= b.W {
		x1 = b.W - 1
	}
	if x0 > x1 {
		return
	}
	row := b.Data[y*b.WordsPerRow : (y+1)*b.WordsPerRow]
	w0, b0 := x0>>6, uint(x0&63)
	w1, b1 := x1>>6, uint(x1&63)
	startMask := ^uint64(0) << b0
	endMask := ^uint64(0) >> (63 - b1)

	if w0 == w1 {
		mask := startMask & endMask
		if val {
			row[w0] |= mask
		} else {
			row[w0] &^= mask
		}
		return
	}
	if val {
		row[w0] |= startMask
		for w := w0 + 1; w < w1; w++ {
			row[w] = ^uint64(0)
		}
		row[w1] |= endMask
	} else {
		row[w0] &^= startMask
		for w := w0 + 1; w < w1; w++ {
			row[w] = 0
		}
		row[w1] &^= endMask
	}
}

/*
parallelRows partitions a vertical row range from zero to h across the requested number of worker goroutines.
Worker counts are clamped to the inclusive interval from one to h so no goroutine receives an empty slice of work.
The function blocks via a wait group until all horizontal bands have finished processing.
*/
func parallelRows(h, workers int, fn func(yStart, yEnd int)) {
	if workers < 1 {
		workers = 1
	}
	if workers > h {
		workers = h
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		y0 := w * h / workers
		y1 := (w + 1) * h / workers
		go func(a, b int) {
			defer wg.Done()
			fn(a, b)
		}(y0, y1)
	}
	wg.Wait()
}

/*
morphStepH applies a separable 1-pixel horizontal morphological erosion or dilation from src into dst.
Each 64-bit word is shifted left and right with carry bits borrowed from neighboring words, processing 64 pixels in parallel per loop iteration.
When dilate is true, neighboring bits are combined with bitwise OR; otherwise bitwise AND performs erosion.
The last word of every row is masked with LastMask to prevent padding bits from leaking into valid grid cells.
*/
func morphStepH(src, dst *Bitset, dilate bool, workers int) {
	wpr := src.WordsPerRow
	lastMask := src.LastMask
	parallelRows(src.H, workers, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sRow := src.Data[y*wpr : (y+1)*wpr]
			dRow := dst.Data[y*wpr : (y+1)*wpr]
			for i := 0; i < wpr; i++ {
				w := sRow[i]
				var prev, next uint64
				if i > 0 {
					prev = sRow[i-1]
				}
				if i+1 < wpr {
					next = sRow[i+1]
				}
				left := (w << 1) | (prev >> 63)
				right := (w >> 1) | (next << 63)
				if dilate {
					dRow[i] = w | left | right
				} else {
					dRow[i] = w & left & right
				}
			}
			dRow[wpr-1] &= lastMask
		}
	})
}

/*
morphStepV applies a separable 1-pixel vertical morphological erosion or dilation from src into dst.
For erosion, each output word is the bitwise AND of the row above, the current row, and the row below, while the top and bottom border rows are cleared.
For dilation, adjacent rows are merged into the destination row with bitwise OR while respecting grid boundaries.
Processing is distributed across horizontal bands using parallelRows for multi-core throughput.
*/
func morphStepV(src, dst *Bitset, dilate bool, workers int) {
	wpr := src.WordsPerRow
	h := src.H
	parallelRows(h, workers, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dRow := dst.Data[y*wpr : (y+1)*wpr]
			cRow := src.Data[y*wpr : (y+1)*wpr]
			if !dilate {
				if y == 0 || y == h-1 {
					clear(dRow)
					continue
				}
				uRow := src.Data[(y-1)*wpr : y*wpr]
				lRow := src.Data[(y+1)*wpr : (y+2)*wpr]
				for i := 0; i < wpr; i++ {
					dRow[i] = uRow[i] & cRow[i] & lRow[i]
				}
			} else {
				copy(dRow, cRow)
				if y > 0 {
					uRow := src.Data[(y-1)*wpr : y*wpr]
					for i := 0; i < wpr; i++ {
						dRow[i] |= uRow[i]
					}
				}
				if y+1 < h {
					lRow := src.Data[(y+1)*wpr : (y+2)*wpr]
					for i := 0; i < wpr; i++ {
						dRow[i] |= lRow[i]
					}
				}
			}
		}
	})
}

/*
Open executes an in-place morphological opening of the given pixel radius using separable horizontal and vertical passes.
It first performs radius iterations of 3x3 square erosion followed by radius iterations of 3x3 square dilation using a temporary scratch bitset.
This operation suppresses narrow unmapped artifacts such as road corridors and sliver gaps up to roughly twice the radius in width while preserving compact open areas.
If radius is non-positive or the raster is smaller than 3x3 pixels, the function returns without modifying the bitset.
*/
func (b *Bitset) Open(radius, workers int) {
	if radius <= 0 || b.W < 3 || b.H < 3 {
		return
	}
	tmp := NewBitset(b.W, b.H)
	for i := 0; i < radius; i++ {
		morphStepH(b, tmp, false, workers)
		morphStepV(tmp, b, false, workers)
	}
	for i := 0; i < radius; i++ {
		morphStepH(b, tmp, true, workers)
		morphStepV(tmp, b, true, workers)
	}
}

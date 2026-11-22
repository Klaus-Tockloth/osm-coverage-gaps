package raster

import (
	"math"
	"slices"

	"osm-area-gaps/internal/geo"
)

/*
AssembleRings stitches open OpenStreetMap way node sequences into closed topological rings.
Already-closed ways with at least four nodes are emitted directly, while open segments are indexed by their start and end node IDs.
The algorithm greedily extends open chains by matching tail endpoints and reversing segments when required until each ring closes or no matching segment remains.
It returns all successfully closed node-ID rings alongside a count of unclosed chains for completeness diagnostics.
*/
func AssembleRings(ways [][]int64) (rings [][]int64, incomplete int) {
	var open [][]int64
	for _, w := range ways {
		if len(w) < 2 {
			continue
		}
		if len(w) >= 4 && w[0] == w[len(w)-1] {
			rings = append(rings, w)
		} else {
			open = append(open, w)
		}
	}
	if len(open) == 0 {
		return rings, 0
	}

	endpoints := make(map[int64][]int, len(open)*2)
	for i, w := range open {
		start, end := w[0], w[len(w)-1]
		endpoints[start] = append(endpoints[start], i)
		if end != start {
			endpoints[end] = append(endpoints[end], i)
		}
	}
	used := make([]bool, len(open))

	for i := range open {
		if used[i] {
			continue
		}
		used[i] = true
		chain := make([]int64, len(open[i]))
		copy(chain, open[i])

		for chain[0] != chain[len(chain)-1] {
			tail := chain[len(chain)-1]
			nextIdx := -1
			for _, cand := range endpoints[tail] {
				if !used[cand] {
					nextIdx = cand
					break
				}
			}
			if nextIdx == -1 {
				break
			}
			used[nextIdx] = true
			seg := open[nextIdx]
			if seg[0] == tail {
				chain = append(chain, seg[1:]...)
			} else {
				for k := len(seg) - 2; k >= 0; k-- {
					chain = append(chain, seg[k])
				}
			}
		}

		if len(chain) >= 4 && chain[0] == chain[len(chain)-1] {
			rings = append(rings, chain)
		} else {
			incomplete++
		}
	}
	return rings, incomplete
}

/*
ResolveRingsMeters converts rings of OSM node IDs into planar meter coordinate slices using a sorted node index and coordinate table.
Each node ID is located via binary search in neededNodes and checked against NaN sentinels that indicate nodes absent from the PBF extract.
Any ring containing a missing node is discarded and increments the returned missingNodes counter.
Complete rings with at least four vertices are promoted from 32-bit storage precision to 64-bit floating-point coordinates for accurate intersection math.
*/
func ResolveRingsMeters(rings [][]int64, neededNodes []int64, coords []geo.Pt32) (out [][]geo.Pt64, missingNodes int) {
	for _, ring := range rings {
		pts := make([]geo.Pt64, 0, len(ring))
		okRing := true
		for _, nid := range ring {
			idx, ok := slices.BinarySearch(neededNodes, nid)
			if !ok {
				okRing = false
				missingNodes++
				break
			}
			c := coords[idx]
			if math.IsNaN(float64(c.X)) {
				okRing = false
				missingNodes++
				break
			}
			pts = append(pts, geo.Pt64{X: float64(c.X), Y: float64(c.Y)})
		}
		if okRing && len(pts) >= 4 {
			out = append(out, pts)
		}
	}
	return out, missingNodes
}

/*
RasterizeRings scanline-fills one or more polygon rings into the target bitset using the even-odd winding rule.
All vertices are first transformed into pixel space, and polygons whose bounding boxes fall outside the raster are skipped immediately.
For each covered scanline, edge intersections at pixel centers are collected, sorted, and filled in alternating pairs via SetSpan, which naturally handles inner holes.
The caller-supplied xsBuf slice is reused across calls to minimize heap allocations and is returned for subsequent invocations.
*/
func RasterizeRings(bs *Bitset, rings [][]geo.Pt64, originX, originY, invRes float64, setVal bool, xsBuf []float64) []float64 {
	if len(rings) == 0 {
		return xsBuf
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)

	pixRings := make([][]geo.Pt64, len(rings))
	for ri, r := range rings {
		pr := make([]geo.Pt64, len(r))
		for i, p := range r {
			px := (p.X - originX) * invRes
			py := (p.Y - originY) * invRes
			pr[i] = geo.Pt64{X: px, Y: py}
			if px < minX {
				minX = px
			}
			if px > maxX {
				maxX = px
			}
			if py < minY {
				minY = py
			}
			if py > maxY {
				maxY = py
			}
		}
		pixRings[ri] = pr
	}

	if maxX < 0 || minX >= float64(bs.W) || maxY < 0 || minY >= float64(bs.H) {
		return xsBuf
	}

	yStart := max(0, int(math.Floor(minY)))
	yEnd := min(bs.H-1, int(math.Ceil(maxY)))

	for y := yStart; y <= yEnd; y++ {
		yf := float64(y) + 0.5
		xsBuf = xsBuf[:0]
		for _, ring := range pixRings {
			for i := 0; i < len(ring)-1; i++ {
				y0, y1 := ring[i].Y, ring[i+1].Y
				if (y0 <= yf && y1 > yf) || (y1 <= yf && y0 > yf) {
					x := ring[i].X + (yf-y0)*(ring[i+1].X-ring[i].X)/(y1-y0)
					xsBuf = append(xsBuf, x)
				}
			}
		}
		if len(xsBuf) < 2 {
			continue
		}
		slices.Sort(xsBuf)
		for i := 0; i+1 < len(xsBuf); i += 2 {
			x0 := int(math.Ceil(xsBuf[i] - 0.5))
			x1 := int(math.Floor(xsBuf[i+1] - 0.5))
			if x0 <= x1 {
				bs.SetSpan(y, x0, x1, setVal)
			}
		}
	}
	return xsBuf
}

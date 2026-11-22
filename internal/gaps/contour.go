package gaps

import (
	"math/bits"

	"osm-area-gaps/internal/geo"
)

/*
simplifyCollinear removes intermediate vertices that lie on straight horizontal or vertical edge runs along a closed integer grid ring.
It computes the 2D cross product of consecutive edge vectors around the cyclic vertex list and retains only corner points with non-zero turn area.
If fewer than three corner vertices remain, the original ring is returned unchanged to preserve a valid closed polygon.
*/
func simplifyCollinear(ring [][2]int) [][2]int {
	if len(ring) <= 4 {
		return ring
	}
	pts := ring[:len(ring)-1]
	n := len(pts)
	out := make([][2]int, 0, n+1)
	for i := 0; i < n; i++ {
		prev := pts[(i-1+n)%n]
		cur := pts[i]
		next := pts[(i+1)%n]
		dx1, dy1 := cur[0]-prev[0], cur[1]-prev[1]
		dx2, dy2 := next[0]-cur[0], next[1]-cur[1]
		if dx1*dy2-dy1*dx2 != 0 {
			out = append(out, cur)
		}
	}
	if len(out) < 3 {
		return ring
	}
	out = append(out, out[0])
	return out
}

/*
perpDistSq computes the squared Euclidean distance from point p to the finite line segment connecting vertices a and b.
It projects p orthogonally onto the segment direction, clamps the interpolation parameter to the unit interval, and evaluates the squared offset to the closest point.
Avoiding the square root makes this helper fast inside recursive Douglas-Peucker simplification loops.
*/
func perpDistSq(p, a, b [2]int) float64 {
	dx := float64(b[0] - a[0])
	dy := float64(b[1] - a[1])
	lenSq := dx*dx + dy*dy
	if lenSq == 0 {
		ex, ey := float64(p[0]-a[0]), float64(p[1]-a[1])
		return ex*ex + ey*ey
	}
	t := (float64(p[0]-a[0])*dx + float64(p[1]-a[1])*dy) / lenSq
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	projX := float64(a[0]) + t*dx
	projY := float64(a[1]) + t*dy
	ex, ey := float64(p[0])-projX, float64(p[1])-projY
	return ex*ex + ey*ey
}

/*
dpOpen recursively simplifies an open integer polyline using the Ramer-Douglas-Peucker algorithm with a squared tolerance threshold.
It scans all interior vertices to locate the point with maximum squared perpendicular distance from the chord connecting the first and last vertices.
When that distance exceeds epsSq, the chain is split at the farthest vertex and both sub-chains are processed recursively; otherwise only the starting vertex is emitted.
Omitting the trailing vertex in the base case prevents duplicate seam vertices when consecutive sub-chains are concatenated.
*/
func dpOpen(pts [][2]int, epsSq float64, out *[][2]int) {
	n := len(pts)
	if n <= 2 {
		*out = append(*out, pts[0])
		return
	}
	maxD := -1.0
	idx := 0
	for i := 1; i < n-1; i++ {
		if d := perpDistSq(pts[i], pts[0], pts[n-1]); d > maxD {
			maxD = d
			idx = i
		}
	}
	if maxD > epsSq {
		dpOpen(pts[:idx+1], epsSq, out)
		dpOpen(pts[idx:], epsSq, out)
	} else {
		*out = append(*out, pts[0])
	}
}

/*
simplifyClosedRing reduces vertex count on a closed grid ring by combining collinearity pruning with closed-loop Douglas-Peucker simplification.
It finds the vertex farthest from the initial vertex and splits the closed cycle into two open polylines so simplification remains balanced around the entire perimeter.
Both halves are simplified with tolerance eps and rejoined into a closed ring by appending the starting point at the end.
If simplification collapses the ring below four vertices, the pre-simplified collinear ring is returned instead.
*/
func simplifyClosedRing(ring [][2]int, eps float64) [][2]int {
	ring = simplifyCollinear(ring)
	if len(ring) <= 5 || eps <= 0 {
		return ring
	}
	farIdx := 1
	maxD := -1
	p0 := ring[0]
	for i := 1; i < len(ring)-1; i++ {
		dx, dy := ring[i][0]-p0[0], ring[i][1]-p0[1]
		if d := dx*dx + dy*dy; d > maxD {
			maxD = d
			farIdx = i
		}
	}
	epsSq := eps * eps
	var out [][2]int
	dpOpen(ring[:farIdx+1], epsSq, &out)
	dpOpen(ring[farIdx:], epsSq, &out)
	out = append(out, out[0])
	if len(out) < 4 {
		return ring
	}
	return out
}

/*
signedArea2 calculates twice the signed planar area of a closed integer polygon ring using the shoelace formula.
A positive value indicates a counter-clockwise exterior boundary in standard south-to-north grid coordinates, whereas a negative value denotes a clockwise interior hole.
Keeping the calculation in 64-bit integer arithmetic avoids rounding error and prevents overflow on large raster components.
*/
func signedArea2(ring [][2]int) int64 {
	var sum int64
	for i := 0; i < len(ring)-1; i++ {
		sum += int64(ring[i][0])*int64(ring[i+1][1]) - int64(ring[i+1][0])*int64(ring[i][1])
	}
	return sum
}

/*
traceComponentRings extracts exact GeoJSON polygon rings from a 4-connected pixel component using a directed dual-edge grid.
Exposed pixel borders are encoded as 4-bit outgoing direction masks per grid corner so exterior boundaries run counter-clockwise and interior holes run clockwise.
Loop traversal prefers right turns at ambiguous saddle vertices to cleanly decompose figure-eight contacts into simple non-crossing rings before simplifying each loop.
Finally, grid corner coordinates are converted back to planar meters and inverse-projected into rounded WGS84 longitude-latitude pairs with the outer ring placed first.
*/
func traceComponentRings(
	cells []geo.Cell,
	minX, minY, maxX, maxY int,
	originX, originY, res float64,
	proj geo.LAEA,
) [][][2]float64 {
	lw := maxX - minX + 1
	lh := maxY - minY + 1
	has := make([]bool, lw*lh)
	for _, c := range cells {
		has[(int(c.Y)-minY)*lw+(int(c.X)-minX)] = true
	}

	vw := lw + 1
	vh := lh + 1
	outMask := make([]uint8, vw*vh)

	for ly := 0; ly < lh; ly++ {
		for lx := 0; lx < lw; lx++ {
			if !has[ly*lw+lx] {
				continue
			}
			if ly == 0 || !has[(ly-1)*lw+lx] {
				outMask[ly*vw+lx] |= 1 << 0
			}
			if lx == lw-1 || !has[ly*lw+(lx+1)] {
				outMask[ly*vw+(lx+1)] |= 1 << 1
			}
			if ly == lh-1 || !has[(ly+1)*lw+lx] {
				outMask[(ly+1)*vw+(lx+1)] |= 1 << 2
			}
			if lx == 0 || !has[ly*lw+(lx-1)] {
				outMask[(ly+1)*vw+lx] |= 1 << 3
			}
		}
	}

	dx := [4]int{1, 0, -1, 0}
	dy := [4]int{0, 1, 0, -1}

	var outerRing [][2]int
	var holeRings [][][2]int

	for vy := 0; vy < vh; vy++ {
		for vx := 0; vx < vw; vx++ {
			startV := vy*vw + vx
			for outMask[startV] != 0 {
				startDir := bits.TrailingZeros8(outMask[startV])
				outMask[startV] &^= 1 << uint(startDir)

				ring := [][2]int{{vx, vy}}
				cx, cy := vx+dx[startDir], vy+dy[startDir]
				dir := startDir

				for {
					ring = append(ring, [2]int{cx, cy})
					cv := cy*vw + cx
					cands := [3]int{(dir + 3) & 3, dir, (dir + 1) & 3}
					closed := false
					moved := false
					for _, nd := range cands {
						if cx == vx && cy == vy && nd == startDir {
							closed = true
							break
						}
						if outMask[cv]&(1<<uint(nd)) != 0 {
							outMask[cv] &^= 1 << uint(nd)
							dir = nd
							cx += dx[dir]
							cy += dy[dir]
							moved = true
							break
						}
					}
					if closed || !moved {
						break
					}
				}

				if len(ring) >= 4 {
					area2 := signedArea2(ring)
					simp := simplifyClosedRing(ring, 0.6)
					if area2 > 0 {
						outerRing = simp
					} else if area2 < 0 {
						holeRings = append(holeRings, simp)
					}
				}
			}
		}
	}

	toLonLat := func(r [][2]int) [][2]float64 {
		resRing := make([][2]float64, len(r))
		for i, p := range r {
			mx := originX + float64(minX+p[0])*res
			my := originY + float64(minY+p[1])*res
			lat, lon := proj.Inverse(mx, my)
			resRing[i] = [2]float64{geo.Round6(lon), geo.Round6(lat)}
		}
		return resRing
	}

	var geoRings [][][2]float64
	if len(outerRing) >= 4 {
		geoRings = append(geoRings, toLonLat(outerRing))
		for _, h := range holeRings {
			geoRings = append(geoRings, toLonLat(h))
		}
	}
	return geoRings
}

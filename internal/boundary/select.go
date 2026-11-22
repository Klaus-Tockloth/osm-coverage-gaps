package boundary

import (
	"fmt"
	"math"

	"github.com/paulmach/osm"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/pbf"
	"osm-area-gaps/internal/raster"
)

// Options controls explicit administrative boundary selection.
type Options struct {
	BoundaryRel  int64
	BoundaryName string
}

/*
Select chooses the analysis boundary polygon rings from candidate administrative relations or falls back to bounding-box rectangles.
Each candidate relation is assembled and resolved into meter coordinates while counting missing ways, open chains, and absent nodes to separate complete from partial boundaries.
Explicitly requested boundaries by ID or name take precedence, followed by automatic selection of the lowest complete admin_level with the largest bounding-box area.
When no usable administrative relation exists in the extract, the function falls back to the PBF header bounds and ultimately to the extent of all loaded node coordinates.
*/
func Select(
	opts Options,
	hdrBounds *osm.Bounds,
	proj geo.LAEA,
	boundaryRels []pbf.RelInfo,
	relWays map[int64][]int64,
	neededNodes []int64,
	coords []geo.Pt32,
) ([][]geo.Pt64, string) {
	type candidate struct {
		info       pbf.RelInfo
		rings      [][]geo.Pt64
		area       float64
		incomplete int
	}

	var complete []candidate
	var partial []candidate

	for _, rel := range boundaryRels {
		var ways [][]int64
		missingWays := 0
		for _, wid := range rel.WayIDs {
			if w, ok := relWays[wid]; ok {
				ways = append(ways, w)
			} else {
				missingWays++
			}
		}
		assembled, inc := raster.AssembleRings(ways)
		rings, missingNodes := raster.ResolveRingsMeters(assembled, neededNodes, coords)
		if len(rings) == 0 {
			continue
		}
		minX, minY, maxX, maxY := geo.RingsBBox(rings)
		cand := candidate{
			info:       rel,
			rings:      rings,
			area:       (maxX - minX) * (maxY - minY),
			incomplete: missingWays + inc + missingNodes,
		}
		if cand.incomplete == 0 {
			complete = append(complete, cand)
		} else {
			partial = append(partial, cand)
		}
	}

	if opts.BoundaryRel > 0 || opts.BoundaryName != "" {
		pool := complete
		if len(pool) == 0 {
			pool = partial
		}
		if len(pool) > 0 {
			best := pool[0]
			for _, c := range pool[1:] {
				if c.area > best.area {
					best = c
				}
			}
			label := fmt.Sprintf("Relation %d (%s, admin_level=%d)", best.info.ID, best.info.Name, best.info.AdminLevel)
			if best.incomplete > 0 {
				label += fmt.Sprintf(" [WARNING: %d incomplete parts]", best.incomplete)
			}
			return best.rings, label
		}
	}

	if len(complete) > 0 {
		best := complete[0]
		for _, c := range complete[1:] {
			if c.info.AdminLevel < best.info.AdminLevel ||
				(c.info.AdminLevel == best.info.AdminLevel && c.area > best.area) {
				best = c
			}
		}
		return best.rings, fmt.Sprintf("Auto: Relation %d (%s, admin_level=%d)", best.info.ID, best.info.Name, best.info.AdminLevel)
	}

	if hdrBounds != nil && hdrBounds.MaxLat > hdrBounds.MinLat && hdrBounds.MaxLon > hdrBounds.MinLon {
		x0, y0 := proj.Forward(hdrBounds.MinLat, hdrBounds.MinLon)
		x1, y1 := proj.Forward(hdrBounds.MinLat, hdrBounds.MaxLon)
		x2, y2 := proj.Forward(hdrBounds.MaxLat, hdrBounds.MaxLon)
		x3, y3 := proj.Forward(hdrBounds.MaxLat, hdrBounds.MinLon)
		rect := [][]geo.Pt64{{{X: x0, Y: y0}, {X: x1, Y: y1}, {X: x2, Y: y2}, {X: x3, Y: y3}, {X: x0, Y: y0}}}
		return rect, "Fallback: PBF header bounding box"
	}

	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range coords {
		if math.IsNaN(float64(c.X)) {
			continue
		}
		x, y := float64(c.X), float64(c.Y)
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	rect := [][]geo.Pt64{{{X: minX, Y: minY}, {X: maxX, Y: minY}, {X: maxX, Y: maxY}, {X: minX, Y: maxY}, {X: minX, Y: minY}}}
	return rect, "Fallback: data bounding box"
}

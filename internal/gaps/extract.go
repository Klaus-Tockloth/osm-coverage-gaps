package gaps

import (
	"cmp"
	"math"
	"math/bits"
	"slices"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/raster"
)

// Result describes a ranked unmapped gap polygon and its geographic metrics.
type Result struct {
	Rank      int
	Pixels    int
	AreaHa    float64
	CenterLat float64
	CenterLon float64
	MinLat    float64
	MinLon    float64
	MaxLat    float64
	MaxLon    float64
	Rings     [][][2]float64
}

/*
Extract scans the bitset in-place via 4-connected breadth-first flood-fill to isolate all remaining unmapped gap components meeting minPixels.
Each discovered bit is cleared immediately upon discovery, eliminating the need for a separate visited matrix while accumulating component cells, bounding extents, and centroid sums.
Qualifying components trigger the optional onGapCells callback, project their centroids and four bounding-box corners back to WGS84, and trace simplified GeoJSON polygon rings.
The resulting gaps are sorted in descending order by area in hectares and assigned 1-based rank numbers before returning.
*/
func Extract(
	bs *raster.Bitset,
	minPixels int,
	originX, originY, res float64,
	proj geo.LAEA,
	onGapCells func([]geo.Cell),
) []Result {
	if bs.W > math.MaxInt32 || bs.H > math.MaxInt32 {
		return nil
	}
	var results []Result
	var queue []geo.Cell
	var comp []geo.Cell
	cellHa := (res * res) / 10000.0

	for y := 0; y < bs.H; y++ {
		if y < 0 || y > math.MaxInt32 {
			break
		}
		rowOff := y * bs.WordsPerRow
		for wi := 0; wi < bs.WordsPerRow; wi++ {
			for {
				word := bs.Data[rowOff+wi]
				if word == 0 {
					break
				}
				bit := bits.TrailingZeros64(word)
				sx := wi*64 + bit
				if sx < 0 || sx > math.MaxInt32 {
					break
				}
				bs.Clear(sx, y)

				queue = append(queue[:0], geo.Cell{X: int32(sx), Y: int32(y)})
				comp = comp[:0]
				minX, maxX := sx, sx
				minY, maxY := y, y
				var sumX, sumY int64

				for head := 0; head < len(queue); head++ {
					cur := queue[head]
					comp = append(comp, cur)
					cx, cy := int(cur.X), int(cur.Y)
					sumX += int64(cx)
					sumY += int64(cy)
					if cx < minX {
						minX = cx
					}
					if cx > maxX {
						maxX = cx
					}
					if cy < minY {
						minY = cy
					}
					if cy > maxY {
						maxY = cy
					}

					if cx > 0 && bs.Get(cx-1, cy) {
						bs.Clear(cx-1, cy)
						queue = append(queue, geo.Cell{X: cur.X - 1, Y: cur.Y})
					}
					if cx+1 < bs.W && bs.Get(cx+1, cy) {
						bs.Clear(cx+1, cy)
						queue = append(queue, geo.Cell{X: cur.X + 1, Y: cur.Y})
					}
					if cy > 0 && bs.Get(cx, cy-1) {
						bs.Clear(cx, cy-1)
						queue = append(queue, geo.Cell{X: cur.X, Y: cur.Y - 1})
					}
					if cy+1 < bs.H && bs.Get(cx, cy+1) {
						bs.Clear(cx, cy+1)
						queue = append(queue, geo.Cell{X: cur.X, Y: cur.Y + 1})
					}
				}

				if len(comp) < minPixels {
					continue
				}
				if onGapCells != nil {
					onGapCells(comp)
				}

				n := float64(len(comp))
				cxM := originX + (float64(sumX)/n+0.5)*res
				cyM := originY + (float64(sumY)/n+0.5)*res
				cLat, cLon := proj.Inverse(cxM, cyM)

				corners := [4][2]float64{
					{originX + float64(minX)*res, originY + float64(minY)*res},
					{originX + float64(maxX+1)*res, originY + float64(minY)*res},
					{originX + float64(maxX+1)*res, originY + float64(maxY+1)*res},
					{originX + float64(minX)*res, originY + float64(maxY+1)*res},
				}
				minLat, minLon := math.Inf(1), math.Inf(1)
				maxLat, maxLon := math.Inf(-1), math.Inf(-1)
				for _, pt := range corners {
					la, lo := proj.Inverse(pt[0], pt[1])
					if la < minLat {
						minLat = la
					}
					if la > maxLat {
						maxLat = la
					}
					if lo < minLon {
						minLon = lo
					}
					if lo > maxLon {
						maxLon = lo
					}
				}

				rings := traceComponentRings(comp, minX, minY, maxX, maxY, originX, originY, res, proj)
				if len(rings) == 0 {
					continue
				}

				results = append(results, Result{
					Pixels:    len(comp),
					AreaHa:    n * cellHa,
					CenterLat: geo.Round6(cLat),
					CenterLon: geo.Round6(cLon),
					MinLat:    geo.Round6(minLat),
					MinLon:    geo.Round6(minLon),
					MaxLat:    geo.Round6(maxLat),
					MaxLon:    geo.Round6(maxLon),
					Rings:     rings,
				})
			}
		}
	}

	slices.SortFunc(results, func(a, b Result) int {
		return cmp.Compare(b.AreaHa, a.AreaHa)
	})
	for i := range results {
		results[i].Rank = i + 1
	}
	return results
}

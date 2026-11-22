package export

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"math/bits"
	"os"

	"osm-area-gaps/internal/geo"
	"osm-area-gaps/internal/raster"
)

// PreviewMap holds a downscaled RGBA diagnostic overview image of the raster analysis.
type PreviewMap struct {
	img    *image.RGBA
	W, H   int
	gw, gh int
}

/*
NewPreviewMap creates a scaled RGBA debug canvas representing a raster grid of dimensions gridW by gridH.
When either raster dimension exceeds 1800 pixels, the preview dimensions are scaled down proportionally to keep image generation fast and file sizes manageable.
Every pixel is initialized with a dark slate background color representing areas outside the active analysis boundary.
The returned PreviewMap tracks both raster and image dimensions for subsequent layered rendering passes.
*/
func NewPreviewMap(gridW, gridH int) *PreviewMap {
	const maxDim = 1800
	pw, ph := gridW, gridH
	if pw > maxDim || ph > maxDim {
		scale := float64(maxDim) / float64(max(pw, ph))
		pw = max(1, int(math.Round(float64(pw)*scale)))
		ph = max(1, int(math.Round(float64(ph)*scale)))
	}
	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	bg := color.RGBA{24, 28, 36, 255}
	for y := 0; y < ph; y++ {
		for x := 0; x < pw; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	return &PreviewMap{img: img, W: pw, H: ph, gw: gridW, gh: gridH}
}

/*
PaintBitset renders all currently set bits of a raster bitset onto the preview canvas using the supplied RGBA color.
It iterates efficiently over non-zero 64-bit words via trailing-zero bit scanning so sparse bitsets are drawn rapidly.
Row indices are flipped vertically because raster Y coordinates increase northward while PNG image coordinates increase downward.
Multiple layered calls allow visualizing covered land, sub-threshold gaps, and final detected gaps in distinct colors.
*/
func (p *PreviewMap) PaintBitset(bs *raster.Bitset, col color.RGBA) {
	for y := 0; y < bs.H; y++ {
		py := (bs.H - 1 - y) * p.H / bs.H
		if py < 0 {
			py = 0
		} else if py >= p.H {
			py = p.H - 1
		}
		rowOff := y * bs.WordsPerRow
		for wi := 0; wi < bs.WordsPerRow; wi++ {
			word := bs.Data[rowOff+wi]
			for word != 0 {
				bit := bits.TrailingZeros64(word)
				x := wi*64 + bit
				px := x * p.W / bs.W
				if px >= p.W {
					px = p.W - 1
				}
				p.img.SetRGBA(px, py, col)
				word &= word - 1
			}
		}
	}
}

/*
PaintCells projects an explicit slice of raster cell coordinates onto the preview canvas in the specified color.
Each cell coordinate is scaled to preview space with vertical north-up inversion and guarded by bounds checks.
During gap extraction, this method highlights qualifying unmapped components above the minimum hectare threshold in red.
*/
func (p *PreviewMap) PaintCells(cells []geo.Cell, col color.RGBA) {
	for _, c := range cells {
		px := int(c.X) * p.W / p.gw
		py := (p.gh - 1 - int(c.Y)) * p.H / p.gh
		if px >= 0 && px < p.W && py >= 0 && py < p.H {
			p.img.SetRGBA(px, py, col)
		}
	}
}

/*
Save encodes the accumulated RGBA preview canvas as a PNG image at the given file path.
It creates or overwrites the destination file and ensures the file descriptor is closed before returning.
Any file creation or PNG encoding error is returned directly to the caller.
*/
func (p *PreviewMap) Save(path string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return png.Encode(f, p.img)
}

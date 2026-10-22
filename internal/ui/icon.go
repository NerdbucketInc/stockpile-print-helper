package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// Dot colours for the tray icon: connected, paused, can't reach, not paired.
var (
	DotGreen = color.RGBA{0x3f, 0xb2, 0x7f, 0xff}
	DotAmber = color.RGBA{0xe0, 0xa5, 0x26, 0xff}
	DotRed   = color.RGBA{0xd5, 0x55, 0x55, 0xff}
	DotGrey  = color.RGBA{0x99, 0x99, 0x99, 0xff}
)

var accent = color.RGBA{0x2f, 0x5d, 0x8a, 0xff}

// IconPNG draws the helper's icon — a blue square with a white "S" — at
// size×size, with a status dot in the corner when dot isn't nil. Drawn here,
// so the program carries no image files.
func IconPNG(size int, dot *color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	u := float64(size) / 32
	fill := func(x0, y0, x1, y1 float64, c color.RGBA) {
		for y := int(y0 * u); y < int(y1*u); y++ {
			for x := int(x0 * u); x < int(x1*u); x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	// The square, its corners cut.
	fill(2, 1, 30, 31, accent)
	fill(1, 2, 31, 30, accent)
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	// A blocky S: top bar, upper left, middle bar, lower right, bottom bar.
	fill(9, 6, 24, 9.5, white)
	fill(8, 7, 11.5, 15, white)
	fill(9, 13.5, 23, 17, white)
	fill(20.5, 15, 24, 24, white)
	fill(8, 22.5, 23, 26, white)
	if dot != nil {
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				dx, dy := float64(x)/u-25.5, float64(y)/u-25.5
				r := dx*dx + dy*dy
				switch {
				case r <= 30:
					img.SetRGBA(x, y, *dot)
				case r <= 42:
					img.SetRGBA(x, y, color.RGBA{0x1c, 0x24, 0x30, 0xff})
				}
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

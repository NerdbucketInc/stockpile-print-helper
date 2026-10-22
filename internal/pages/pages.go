// Package pages reads a page job: PNG pages Stockpile drew from a PDF. The
// helper has no PDF reader — only Go's own PNG decoder (memory-safe), with
// the size checked before a page is decoded.
package pages

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// Limits: what Stockpile draws at most, with room.
const (
	MaxPages  = 20
	MaxWidth  = 5000
	MaxHeight = 7000
)

// Page is one page, decoded to 8-bit grey, with its size on paper.
type Page struct {
	Gray     *image.Gray
	WidthMM  float64
	HeightMM float64
	DPI      int
}

type wire struct {
	Pages []struct {
		PNG      string  `json:"png"`
		WidthMM  float64 `json:"widthMm"`
		HeightMM float64 `json:"heightMm"`
		DPI      int     `json:"dpi"`
	} `json:"pages"`
}

// Decode reads a page job's body.
func Decode(body []byte) ([]Page, error) {
	var w wire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("the pages couldn't be read")
	}
	if len(w.Pages) == 0 || len(w.Pages) > MaxPages {
		return nil, fmt.Errorf("a page job must have 1 to %d pages", MaxPages)
	}
	out := make([]Page, 0, len(w.Pages))
	for i, p := range w.Pages {
		raw, err := base64.StdEncoding.DecodeString(p.PNG)
		if err != nil {
			return nil, fmt.Errorf("page %d isn't readable", i+1)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("page %d isn't a PNG", i+1)
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxWidth || cfg.Height > MaxHeight {
			return nil, fmt.Errorf("page %d is too big (%d×%d)", i+1, cfg.Width, cfg.Height)
		}
		if !(p.WidthMM > 0 && p.WidthMM < 2000 && p.HeightMM > 0 && p.HeightMM < 5000) {
			return nil, fmt.Errorf("page %d has no size on paper", i+1)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("page %d isn't a PNG", i+1)
		}
		out = append(out, Page{Gray: toGray(img), WidthMM: p.WidthMM, HeightMM: p.HeightMM, DPI: p.DPI})
	}
	return out, nil
}

func toGray(img image.Image) *image.Gray {
	if g, ok := img.(*image.Gray); ok && g.Rect.Min == (image.Point{}) {
		return g
	}
	b := img.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	// Transparent areas print as paper (white).
	draw.Draw(g, g.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(g, g.Bounds(), img, b.Min, draw.Over)
	return g
}

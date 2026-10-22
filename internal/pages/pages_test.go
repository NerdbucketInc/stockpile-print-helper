package pages

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"
)

func pageJSON(t *testing.T, w, h, n int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	type pg struct {
		PNG      string  `json:"png"`
		WidthMM  float64 `json:"widthMm"`
		HeightMM float64 `json:"heightMm"`
		DPI      int     `json:"dpi"`
	}
	var ps []pg
	for i := 0; i < n; i++ {
		ps = append(ps, pg{base64.StdEncoding.EncodeToString(buf.Bytes()), 101.6, 152.4, 203})
	}
	b, _ := json.Marshal(map[string]any{"pages": ps})
	return b
}

func TestDecode(t *testing.T) {
	ps, err := Decode(pageJSON(t, 812, 1218, 2))
	if err != nil || len(ps) != 2 || ps[0].Gray.Rect.Dx() != 812 || ps[0].WidthMM != 101.6 {
		t.Fatalf("got %d pages, %v", len(ps), err)
	}
}

func TestRefusals(t *testing.T) {
	for name, body := range map[string][]byte{
		"not json":  []byte("%PDF-1.4"),
		"no pages":  []byte(`{"pages":[]}`),
		"too many":  pageJSON(t, 10, 10, MaxPages+1),
		"too big":   pageJSON(t, MaxWidth+1, 10, 1),
		"not a png": []byte(`{"pages":[{"png":"aGVsbG8=","widthMm":10,"heightMm":10}]}`),
		"no size":   bytes.Replace(pageJSON(t, 10, 10, 1), []byte(`"widthMm":101.6`), []byte(`"widthMm":0`), 1),
	} {
		if _, err := Decode(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

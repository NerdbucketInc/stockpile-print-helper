//go:build devsink

package printers

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/api"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/config"
	"github.com/nerdbucketinc/stockpile-print-helper/internal/pages"
)

// DevSinkBuilt: this is a DEVELOPMENT build (tag devsink). It has a "File
// printer" that writes each job to a folder — for the end-to-end test only.
// Release builds are made without the tag and can't write a document.
const DevSinkBuilt = true

type sink struct{ dir string }

func devSink(s config.Settings) []Printer {
	if s.SinkDir == "" {
		return nil
	}
	return []Printer{&sink{dir: s.SinkDir}}
}

func (s *sink) Info() api.Printer {
	return api.Printer{ID: "sink:page", Name: "File printer (development build)", Connection: "file", Kind: "page", Formats: []string{FormatPNG, FormatZPL}, DPI: 150, Papers: []string{"Letter"}, Status: "ready"}
}

var notSafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *sink) name(title, ext string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), notSafe.ReplaceAllString(title, "_"), ext))
}

func (s *sink) PrintRaw(_ context.Context, title string, data []byte, copies int) Outcome {
	if err := os.WriteFile(s.name(title, ".zpl"), data, 0o600); err != nil {
		return Outcome{State: "failed", Code: "spooler", Message: err.Error()}
	}
	return Outcome{State: "printed"}
}

func (s *sink) PrintPages(_ context.Context, title string, p []pages.Page, copies int) Outcome {
	for i, pg := range p {
		f, err := os.Create(s.name(fmt.Sprintf("%s-p%d", title, i+1), ".png"))
		if err != nil {
			return Outcome{State: "failed", Code: "spooler", Message: err.Error()}
		}
		err = png.Encode(f, pg.Gray)
		f.Close()
		if err != nil {
			return Outcome{State: "uncertain", Message: err.Error()}
		}
	}
	return Outcome{State: "printed"}
}

// Package printers is what the helper can print on: printers installed in
// Windows, and network printers someone kept at this computer. Stockpile
// picks from this list by id; it can never name an address or add a printer.
package printers

import (
	"context"
	"regexp"
	"strings"

	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/api"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/config"
	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/pages"
)

// Formats a printer takes, as reported to Stockpile.
const (
	FormatZPL = "ZPL" // label data, sent as it is
	FormatPNG = "PNG" // pages drawn by Stockpile
)

// Outcome is how one job went, in the words Stockpile takes.
type Outcome struct {
	State   string // printed | sent | failed | uncertain
	Code    string // paper_out | offline | format | spooler | …
	Message string
}

// Printer is one printer this helper may use.
type Printer interface {
	Info() api.Printer
	// PrintRaw sends label data as it is. Only for a printer that takes ZPL.
	PrintRaw(ctx context.Context, title string, data []byte, copies int) Outcome
	// PrintPages prints PNG pages. Only for a printer that takes PNG.
	PrintPages(ctx context.Context, title string, p []pages.Page, copies int) Outcome
}

// Takes reports whether p takes a format.
func Takes(p Printer, format string) bool {
	for _, f := range p.Info().Formats {
		if f == format {
			return true
		}
	}
	return false
}

// All is every printer: Windows' own (on Windows), the network printers kept
// in settings, and a development build's file printer.
func All(ctx context.Context, s config.Settings) []Printer {
	var out []Printer
	out = append(out, installed(ctx)...)
	for _, n := range s.Network {
		if p, err := NewNetwork(n); err == nil {
			out = append(out, p)
		}
	}
	out = append(out, devSink(s)...)
	return out
}

// ByID is the printer with this id, or nil.
func ByID(list []Printer, id string) Printer {
	for _, p := range list {
		if p.Info().ID == id {
			return p
		}
	}
	return nil
}

var (
	labelDriver   = regexp.MustCompile(`(?i)zdesigner|zebra|\bzpl\b|\bzd[0-9]{3}|gk420|gx4[23]0|lp ?2844|tlp ?2844|gc420|zt[0-9]{3}`)
	eplDriver     = regexp.MustCompile(`(?i)(epl)|epl2?`)
	receiptDriver = regexp.MustCompile(`(?i)\btm-|receipt|pos-?80|pos-?58|star tsp|tsp1|srp-|ct-s[0-9]|thermal receipt`)
)

// KindOf guesses what a Windows printer is from its driver and name.
func KindOf(driver, name string) (kind string, zpl bool) {
	both := driver + " " + name
	switch {
	case eplDriver.MatchString(both):
		// An EPL label printer: label data in ZPL would print as text, so pages only.
		return "label", false
	case labelDriver.MatchString(both):
		return "label", true
	case receiptDriver.MatchString(both):
		return "receipt", false
	default:
		return "page", false
	}
}

// Skipped are Windows' own virtual printers: nothing to print a receipt on.
func Skipped(name, port string) bool {
	n := strings.ToLower(name)
	p := strings.ToLower(port)
	return strings.Contains(n, "microsoft print to pdf") || strings.Contains(n, "xps document writer") ||
		strings.Contains(n, "onenote") || n == "fax" || strings.HasPrefix(p, "portprompt") || p == "nul:" || strings.HasPrefix(p, "shrfax")
}

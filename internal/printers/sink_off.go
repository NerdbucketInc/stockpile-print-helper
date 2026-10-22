//go:build !devsink

package printers

import "github.com/hyperbolic2346/stockpile/apps/print-helper/internal/config"

// A release build has no file printer: documents are never written to disk.
func devSink(config.Settings) []Printer { return nil }

// DevSinkBuilt reports whether this build has the development file printer.
const DevSinkBuilt = false

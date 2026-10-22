//go:build !windows

package printers

import "context"

// Only Windows has installed printers here (a Mac build reads CUPS, later).
func installed(context.Context) []Printer { return nil }

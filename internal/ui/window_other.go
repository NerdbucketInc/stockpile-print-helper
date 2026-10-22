//go:build !windows

package ui

import (
	"errors"

	"github.com/nerdbucketinc/stockpile-print-helper/internal/app"
)

// Run: the window is Windows only (a Mac build gets its own, later). Use the
// commands instead.
func Run(*app.App, bool, string) error {
	return errors.New("the helper's window is Windows only; run `stockpile-print-helper run` instead")
}

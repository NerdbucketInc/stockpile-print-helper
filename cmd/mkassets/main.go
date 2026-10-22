// Command mkassets writes the MSIX package's logos into a folder. They're
// drawn by the same code as the tray icon (internal/ui/icon.go), so the
// repository carries no image files.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyperbolic2346/stockpile/apps/print-helper/internal/ui"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkassets DIR")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for name, size := range map[string]int{"Square44x44Logo.png": 44, "Square150x150Logo.png": 150, "StoreLogo.png": 50} {
		if err := os.WriteFile(filepath.Join(dir, name), ui.IconPNG(size, nil), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

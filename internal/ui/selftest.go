package ui

import (
	"fmt"
	"os"
)

// writeSelftest puts the window's text where the check asked ("-" = stdout).
func writeSelftest(path, text string) error {
	if path == "-" {
		_, err := fmt.Println(text)
		return err
	}
	return os.WriteFile(path, []byte(text+"\n"), 0o600)
}

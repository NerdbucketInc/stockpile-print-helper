// Package secret keeps the device token at rest. On Windows it is encrypted
// with DPAPI for the signed-in user (CryptProtectData), so another Windows
// user — or the file copied to another computer — can't use it. Elsewhere
// (development and tests only) it is a file only its owner can read.
package secret

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNone: no token is saved (not paired).
var ErrNone = errors.New("not paired")

// Save writes the token, protected, replacing any before it.
func Save(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sealed, err := protect([]byte(token))
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads the token back.
func Load(path string) (string, error) {
	sealed, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", ErrNone
	}
	if err != nil {
		return "", err
	}
	b, err := unprotect(sealed)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Forget deletes the token (unpaired or removed).
func Forget(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

//go:build !windows

package secret

// Development and tests only: the file's 0600 permission is the protection.
func protect(plain []byte) ([]byte, error) { return append([]byte{}, plain...), nil }

func unprotect(sealed []byte) ([]byte, error) { return append([]byte{}, sealed...), nil }

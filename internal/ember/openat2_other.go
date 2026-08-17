//go:build !linux

package ember

import (
	"os"
	"path/filepath"
)

func confinementAvailable(root string) bool { return false }

func providerOpen(root, rel string, flags int, mode os.FileMode, production bool) (*os.File, error) {
	if production {
		return nil, ErrProvider
	}
	return os.OpenFile(filepath.Join(root, rel), flags, mode)
}

func providerPathCheck(root, rel string, production bool) error {
	if production {
		return ErrProvider
	}
	return nil
}

func mkdirAllWithinRoot(root, rel string, mode os.FileMode) error {
	return os.MkdirAll(filepath.Join(root, rel), mode)
}

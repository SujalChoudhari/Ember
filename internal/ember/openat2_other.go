//go:build !linux

package ember

import (
	"os"
	"path/filepath"
)

func confinementAvailable(rootPath string) bool { return false }

func providerOpen(rootPath, relativePath string, flags int, mode os.FileMode, production bool) (*os.File, error) {
	if production {
		return nil, ErrProvider
	}
	return os.OpenFile(filepath.Join(rootPath, relativePath), flags, mode)
}

func providerPathCheck(rootPath, relativePath string, production bool) error {
	if production {
		return ErrProvider
	}
	return nil
}

func mkdirAllWithinRoot(rootPath, relativePath string, mode os.FileMode) error {
	return os.MkdirAll(filepath.Join(rootPath, relativePath), mode)
}

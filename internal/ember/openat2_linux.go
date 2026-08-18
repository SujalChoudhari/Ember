//go:build linux

package ember

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	resolveNoSymlinks = 0x02
	resolveBeneath    = 0x08
	sysOpenat2        = 437
	oPath             = 0x200000
)

type openHow struct {
	flags   uint64
	mode    uint64
	resolve uint64
}

func openWithinRoot(rootPath, relativePath string, flags int, mode uint32) (*os.File, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) || strings.HasPrefix(relativePath, "../") || strings.Contains(relativePath, "/../") || relativePath == ".." {
		return nil, ErrPathUnsafe
	}
	rootFileDescriptor, err := syscall.Open(rootPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(rootFileDescriptor)
	relativePathBytes := []byte(relativePath + "\x00")
	openParameters := openHow{flags: uint64(flags) | uint64(syscall.O_CLOEXEC), mode: uint64(mode), resolve: resolveBeneath | resolveNoSymlinks}
	fileDescriptor, _, errno := syscall.Syscall6(sysOpenat2, uintptr(rootFileDescriptor), uintptr(unsafe.Pointer(&relativePathBytes[0])), uintptr(unsafe.Pointer(&openParameters)), unsafe.Sizeof(openParameters), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	return os.NewFile(fileDescriptor, filepath.Join(rootPath, relativePath)), nil
}

func mkdirAllWithinRoot(rootPath, relativePath string, mode os.FileMode) error {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return ErrPathUnsafe
	}
	if _, err := openWithinRoot(rootPath, relativePath, oPath|syscall.O_DIRECTORY, 0); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(rootPath, relativePath), mode); err != nil {
		return err
	}
	checkFile, err := openWithinRoot(rootPath, relativePath, oPath|syscall.O_DIRECTORY, 0)
	if checkFile != nil {
		_ = checkFile.Close()
	}
	return err
}

func providerOpen(rootPath, relativePath string, flags int, mode os.FileMode, production bool) (*os.File, error) {
	if production {
		return openWithinRoot(rootPath, relativePath, flags, uint32(mode.Perm()))
	}
	return os.OpenFile(filepath.Join(rootPath, relativePath), flags, mode)
}

func providerPathCheck(rootPath, relativePath string, production bool) error {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return ErrPathUnsafe
	}
	if !production {
		_, err := os.Lstat(filepath.Join(rootPath, relativePath))
		return err
	}
	checkedFile, err := openWithinRoot(rootPath, relativePath, oPath, 0)
	if checkedFile != nil {
		_ = checkedFile.Close()
	}
	return err
}

func confinementAvailable(rootPath string) bool {
	rootFileDescriptor, err := syscall.Open(rootPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(rootFileDescriptor)
	relativePathBytes := []byte(".\x00")
	openParameters := openHow{flags: uint64(oPath), resolve: resolveBeneath | resolveNoSymlinks}
	_, _, errno := syscall.Syscall6(sysOpenat2, uintptr(rootFileDescriptor), uintptr(unsafe.Pointer(&relativePathBytes[0])), uintptr(unsafe.Pointer(&openParameters)), unsafe.Sizeof(openParameters), 0, 0)
	return errno == 0
}

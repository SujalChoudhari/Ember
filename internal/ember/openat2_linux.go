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
	sysMkdirat        = 258
	oPath             = 0x200000
)

type openHow struct {
	flags   uint64
	mode    uint64
	resolve uint64
}

func openWithinRoot(root, rel string, flags int, mode uint32) (*os.File, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || rel == ".." {
		return nil, ErrPathUnsafe
	}
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(rootFD)
	path := []byte(rel + "\x00")
	how := openHow{flags: uint64(flags) | uint64(syscall.O_CLOEXEC), mode: uint64(mode), resolve: resolveBeneath | resolveNoSymlinks}
	fd, _, errno := syscall.Syscall6(sysOpenat2, uintptr(rootFD), uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	return os.NewFile(fd, filepath.Join(root, rel)), nil
}

func mkdirAllWithinRoot(root, rel string, mode os.FileMode) error {
	if rel == "" || filepath.IsAbs(rel) {
		return ErrPathUnsafe
	}
	if _, err := openWithinRoot(root, rel, oPath|syscall.O_DIRECTORY, 0); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, rel), mode); err != nil {
		return err
	}
	check, err := openWithinRoot(root, rel, oPath|syscall.O_DIRECTORY, 0)
	if check != nil {
		_ = check.Close()
	}
	return err
}

func providerOpen(root, rel string, flags int, mode os.FileMode, production bool) (*os.File, error) {
	if production {
		return openWithinRoot(root, rel, flags, uint32(mode.Perm()))
	}
	return os.OpenFile(filepath.Join(root, rel), flags, mode)
}

func providerPathCheck(root, rel string, production bool) error {
	if rel == "" || filepath.IsAbs(rel) {
		return ErrPathUnsafe
	}
	if !production {
		_, err := os.Lstat(filepath.Join(root, rel))
		return err
	}
	f, err := openWithinRoot(root, rel, oPath, 0)
	if f != nil {
		_ = f.Close()
	}
	return err
}

func confinementAvailable(root string) bool {
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	path := []byte(".\x00")
	how := openHow{flags: uint64(oPath), resolve: resolveBeneath | resolveNoSymlinks}
	_, _, errno := syscall.Syscall6(sysOpenat2, uintptr(fd), uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	return errno == 0
}

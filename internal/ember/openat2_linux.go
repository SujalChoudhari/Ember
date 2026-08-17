//go:build linux

package ember

import (
	"syscall"
	"unsafe"
)

const (
	resolveNoSymlinks = 0x02
	resolveBeneath    = 0x08
	sysOpenat2         = 437
)

type openHow struct {
	flags   uint64
	mode    uint64
	resolve uint64
}

func confinementAvailable(root string) bool {
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil { return false }
	defer syscall.Close(fd)
	path := []byte(".\x00")
	how := openHow{flags: uint64(syscall.O_PATH), resolve: resolveBeneath | resolveNoSymlinks}
	_, _, errno := syscall.Syscall6(sysOpenat2, uintptr(fd), uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	return errno == 0
}

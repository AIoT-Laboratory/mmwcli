//go:build linux && amd64

package capturefile

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	linuxAMD64Renameat2 = 316
	renameNoReplace     = 1
)

func publishDirectoryNoReplace(partPath, finalPath string) error {
	from, err := syscall.BytePtrFromString(partPath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: partPath, New: finalPath, Err: err}
	}
	to, err := syscall.BytePtrFromString(finalPath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: partPath, New: finalPath, Err: err}
	}
	_, _, errno := syscall.RawSyscall6(
		linuxAMD64Renameat2,
		^uintptr(99),
		uintptr(unsafe.Pointer(from)),
		^uintptr(99),
		uintptr(unsafe.Pointer(to)),
		renameNoReplace,
		0,
	)
	if errno != 0 {
		return &os.LinkError{Op: "rename", Old: partPath, New: finalPath, Err: errno}
	}
	return nil
}

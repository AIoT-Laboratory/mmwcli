//go:build linux

package capturefile

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const renameNoReplace = 1

// publishDirectoryNoReplace uses renameat2 so a destination that appears
// after CreateSessionDirectory is never replaced. Unsupported kernels and
// filesystems fail safely; there is intentionally no os.Rename fallback.
func publishDirectoryNoReplace(partPath, finalPath string) error {
	trap, err := renameat2Trap()
	if err != nil {
		return &os.LinkError{Op: "renameat2", Old: partPath, New: finalPath, Err: err}
	}
	from, err := syscall.BytePtrFromString(partPath)
	if err != nil {
		return &os.LinkError{Op: "renameat2", Old: partPath, New: finalPath, Err: err}
	}
	to, err := syscall.BytePtrFromString(finalPath)
	if err != nil {
		return &os.LinkError{Op: "renameat2", Old: partPath, New: finalPath, Err: err}
	}
	atFDCWD := ^uintptr(99)
	_, _, callErr := syscall.Syscall6(
		trap,
		atFDCWD,
		uintptr(unsafe.Pointer(from)),
		atFDCWD,
		uintptr(unsafe.Pointer(to)),
		renameNoReplace,
		0,
	)
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	if callErr != 0 {
		return &os.LinkError{Op: "renameat2", Old: partPath, New: finalPath, Err: callErr}
	}
	return nil
}

func renameat2Trap() (uintptr, error) {
	switch runtime.GOARCH {
	case "amd64":
		return 316, nil
	case "arm64":
		return 276, nil
	default:
		return 0, fmt.Errorf("unsupported linux architecture %s", runtime.GOARCH)
	}
}

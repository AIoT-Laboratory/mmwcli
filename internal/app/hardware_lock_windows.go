//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func acquireHardwareLock(setupPath string) (func(), error) {
	abs, err := filepath.Abs(setupPath)
	if err != nil {
		return nil, fmt.Errorf("resolve hardware lock: %w", err)
	}
	path := filepath.Join(filepath.Dir(abs), ".hardware.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open hardware lock: %w", err)
	}
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileEx.Call(
		file.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		_ = file.Close()
		return nil, fmt.Errorf("hardware is busy: %w", callErr)
	}
	return func() {
		_, _, _ = unlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		_ = file.Close()
	}, nil
}

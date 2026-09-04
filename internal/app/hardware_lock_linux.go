//go:build linux

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("hardware is busy: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

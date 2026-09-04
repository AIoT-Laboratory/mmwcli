//go:build linux

package app

import (
	"path/filepath"
	"testing"
)

func TestLinuxHardwareLockIsProcessWide(t *testing.T) {
	setup := filepath.Join(t.TempDir(), "setup.json")
	release, err := acquireHardwareLock(setup)
	if err != nil {
		t.Fatalf("acquire first hardware lock: %v", err)
	}
	if _, err := acquireHardwareLock(setup); err == nil {
		release()
		t.Fatal("acquired an already-held hardware lock")
	}
	release()

	releaseAgain, err := acquireHardwareLock(setup)
	if err != nil {
		t.Fatalf("reacquire released hardware lock: %v", err)
	}
	releaseAgain()
}

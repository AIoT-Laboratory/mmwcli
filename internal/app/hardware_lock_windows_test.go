//go:build windows

package app

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupMountSharesTheCaptureHardwareLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	writeTestSetup(t, path, false)
	release, err := acquireHardwareLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = runSetupMount([]string{path, "--height", "1.6", "--pitch", "90"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "hardware is busy") {
		t.Fatalf("concurrent setup mount error = %v", err)
	}
}

func TestSetupROISharesTheCaptureHardwareLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	writeTestSetup(t, path, false)
	release, err := acquireHardwareLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = runSetupROI([]string{
		path,
		"--min-forward", "0.5", "--max-forward", "5.5",
		"--min-lateral", "-1.5", "--max-lateral", "1.5",
		"--min-up", "0", "--max-up", "2.2",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "hardware is busy") {
		t.Fatalf("concurrent setup ROI error = %v", err)
	}
}

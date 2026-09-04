//go:build linux && amd64

package capturefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxPublishDirectoryDoesNotReplace(t *testing.T) {
	root := t.TempDir()
	part := filepath.Join(root, "take.part")
	final := filepath.Join(root, "take")
	if err := os.Mkdir(part, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishDirectoryNoReplace(part, final); err != nil {
		t.Fatalf("publish directory: %v", err)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("stat published directory: %v", err)
	}

	if err := os.Mkdir(part, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishDirectoryNoReplace(part, final); err == nil {
		t.Fatal("replaced an existing capture")
	}
}

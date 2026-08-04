//go:build windows && ftd2xx

package d2xx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstalledLibraryWithoutDeviceAccess(t *testing.T) {
	systemDirectory, err := windowsSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(systemDirectory, "ftd2xx.dll")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skipf("FTDI D2XX is not installed at %s", path)
	} else if err != nil {
		t.Fatal(err)
	}

	library, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	info := library.Info()
	if info.Library != path || !info.VersionKnown || info.Version == 0 {
		t.Fatalf("D2XX info = %+v", info)
	}
	t.Logf("D2XX library=%s version=%s raw=0x%08X", info.Library, info.Version, uint32(info.Version))
}

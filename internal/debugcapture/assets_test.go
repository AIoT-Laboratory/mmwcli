package debugcapture

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAssetsAcceptsMatchingDistinctFiles(t *testing.T) {
	bssContent := []byte("fixture BSS")
	mssContent := []byte("fixture MSS image")
	bssPath := writeAsset(t, "bss.bin", bssContent)
	mssPath := writeAsset(t, "mss.bin", mssContent)

	assets, err := checkAssets(bssPath, mssPath, fixtureContracts(bssContent, mssContent))
	if err != nil {
		t.Fatal(err)
	}
	if assets.BSS.Path != bssPath || assets.MSS.Path != mssPath {
		t.Fatalf("paths = %q, %q", assets.BSS.Path, assets.MSS.Path)
	}
	if assets.BSS.Role != "BSS" || assets.MSS.Role != "MSS" {
		t.Fatalf("roles = %q, %q", assets.BSS.Role, assets.MSS.Role)
	}
}

func TestCheckAssetsRejectsSameFile(t *testing.T) {
	content := []byte("one image")
	path := writeAsset(t, "same.bin", content)
	_, err := checkAssets(path, path, fixtureContracts(content, content))
	if err == nil || !strings.Contains(err.Error(), "must be different files") {
		t.Fatalf("error = %v, want different-files error", err)
	}
}

func TestCheckAssetsRejectsSwappedTruncatedAndModifiedFiles(t *testing.T) {
	bssContent := []byte("fixture BSS")
	mssContent := []byte("fixture MSS image")
	expected := fixtureContracts(bssContent, mssContent)

	for _, test := range []struct {
		name string
		bss  []byte
		mss  []byte
		want string
	}{
		{name: "swapped", bss: mssContent, mss: bssContent, want: "BSS firmware size mismatch"},
		{name: "truncated", bss: bssContent[:len(bssContent)-1], mss: mssContent, want: "BSS firmware size mismatch"},
		{name: "modified", bss: append([]byte(nil), bssContent...), mss: []byte("fixture MSS imagf"), want: "MSS firmware SHA-256 mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bssPath := writeAsset(t, "bss.bin", test.bss)
			mssPath := writeAsset(t, "mss.bin", test.mss)
			_, err := checkAssets(bssPath, mssPath, expected)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCheckAssetsRequiresRegularFiles(t *testing.T) {
	content := []byte("fixture")
	path := writeAsset(t, "mss.bin", content)
	expected := fixtureContracts(content, content)
	for _, test := range []struct {
		name string
		bss  string
	}{
		{name: "empty", bss: ""},
		{name: "directory", bss: t.TempDir()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := checkAssets(test.bss, path, expected); err == nil {
				t.Fatalf("checkAssets accepted BSS path %q", test.bss)
			}
		})
	}
}

func fixtureContracts(bssContent, mssContent []byte) contracts {
	return contracts{
		bss: fileContract{role: "BSS", name: "bss.bin", size: int64(len(bssContent)), sha256: digest(bssContent)},
		mss: fileContract{role: "MSS", name: "mss.bin", size: int64(len(mssContent)), sha256: digest(mssContent)},
	}
}

func digest(content []byte) string {
	hash := sha256.Sum256(content)
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

func writeAsset(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

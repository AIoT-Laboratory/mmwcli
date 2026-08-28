package iwr6843

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAssetsAcceptsMatchingDistinctFiles(t *testing.T) {
	bssContent := makeRPRCFixture(0, fixtureSection{address: 0x1000, data: []byte("BSS!")})
	mssContent := makeRPRCFixture(0x1234, fixtureSection{address: 0x2000, data: []byte("MSS data!!!!")})
	bssPath := writeAsset(t, "bss.bin", bssContent)
	mssPath := writeAsset(t, "mss.bin", mssContent)

	assets, err := verifyAssets(bssPath, mssPath, fixtureContracts(bssContent, mssContent))
	if err != nil {
		t.Fatal(err)
	}
	if assets.BSS.Path != bssPath || assets.MSS.Path != mssPath {
		t.Fatalf("paths = %q, %q", assets.BSS.Path, assets.MSS.Path)
	}
	if assets.BSS.Role != "BSS" || assets.MSS.Role != "MSS" {
		t.Fatalf("roles = %q, %q", assets.BSS.Role, assets.MSS.Role)
	}
	if assets.BSS.Sections != 1 || assets.MSS.Sections != 1 {
		t.Fatalf("section counts = %d, %d", assets.BSS.Sections, assets.MSS.Sections)
	}
	if assets.MSS.EntryPoint != 0x1234 || assets.BSS.Writes != 1 || assets.MSS.Writes != 1 || len(assets.BSS.writePlan) != 1 {
		t.Fatalf("unexpected RPRC metadata: BSS=%+v MSS=%+v", assets.BSS, assets.MSS)
	}
}

func TestCheckAssetsRejectsSameFile(t *testing.T) {
	content := makeRPRCFixture(0, fixtureSection{address: 0x1000, data: []byte("data")})
	path := writeAsset(t, "same.bin", content)
	_, err := verifyAssets(path, path, fixtureContracts(content, content))
	if err == nil || !strings.Contains(err.Error(), "must be different files") {
		t.Fatalf("error = %v, want different-files error", err)
	}
}

func TestCheckAssetsRejectsSwappedTruncatedAndModifiedFiles(t *testing.T) {
	bssContent := makeRPRCFixture(0, fixtureSection{address: 0x1000, data: []byte("BSS!")})
	mssContent := makeRPRCFixture(0x1234, fixtureSection{address: 0x2000, data: []byte("MSS data!!!!")})
	expected := fixtureContracts(bssContent, mssContent)
	modifiedMSS := append([]byte(nil), mssContent...)
	modifiedMSS[len(modifiedMSS)-1] ^= 0x01

	for _, test := range []struct {
		name string
		bss  []byte
		mss  []byte
		want string
	}{
		{name: "swapped", bss: mssContent, mss: bssContent, want: "BSS firmware size mismatch"},
		{name: "truncated", bss: bssContent[:len(bssContent)-1], mss: mssContent, want: "BSS firmware size mismatch"},
		{name: "modified", bss: append([]byte(nil), bssContent...), mss: modifiedMSS, want: "MSS firmware SHA-256 mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bssPath := writeAsset(t, "bss.bin", test.bss)
			mssPath := writeAsset(t, "mss.bin", test.mss)
			_, err := verifyAssets(bssPath, mssPath, expected)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCheckAssetsRequiresRegularFiles(t *testing.T) {
	content := makeRPRCFixture(0, fixtureSection{address: 0x1000, data: []byte("data")})
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
			if _, err := verifyAssets(test.bss, path, expected); err == nil {
				t.Fatalf("checkAssets accepted BSS path %q", test.bss)
			}
		})
	}
}

func fixtureContracts(bssContent, mssContent []byte) contracts {
	return contracts{
		bss: fileContract{role: "BSS", name: "bss.bin", size: int64(len(bssContent)), sha256: digest(bssContent), target: rprcTargetBSS},
		mss: fileContract{role: "MSS", name: "mss.bin", size: int64(len(mssContent)), sha256: digest(mssContent), target: rprcTargetMSS},
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

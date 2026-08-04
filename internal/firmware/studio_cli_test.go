package firmware

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyFileAcceptsMatchingAsset(t *testing.T) {
	content := []byte("fixture firmware")
	digest := sha256.Sum256(content)
	path := filepath.Join(t.TempDir(), "renamed.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := verifyFile(path, expectation{
		size:   int64(len(content)),
		sha256: strings.ToUpper(hex.EncodeToString(digest[:])),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != path {
		t.Fatalf("path = %q, want %q", info.Path, path)
	}
	if info.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", info.Size, len(content))
	}
}

func TestVerifyStudioCLIRejectsSizeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), StudioCLIName)
	if err := os.WriteFile(path, []byte("not TI firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyStudioCLI(path)
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("error = %v, want size mismatch", err)
	}
}

func TestVerifyFileRejectsHashMismatch(t *testing.T) {
	content := []byte("same size")
	path := filepath.Join(t.TempDir(), StudioCLIName)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := verifyFile(path, expectation{
		size:   int64(len(content)),
		sha256: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("error = %v, want SHA-256 mismatch", err)
	}
}

func TestVerifyStudioCLIRequiresFile(t *testing.T) {
	for _, path := range []string{"", t.TempDir()} {
		if _, err := VerifyStudioCLI(path); err == nil {
			t.Fatalf("VerifyStudioCLI(%q) accepted non-file", path)
		}
	}
}

package debugcapture

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	BSSName   = "xwr68xx_radarss.bin"
	BSSSize   = int64(240072)
	BSSSHA256 = "E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F"

	MSSName   = "xwr68xx_masterss.bin"
	MSSSize   = int64(92992)
	MSSSHA256 = "316911D4A8DBA1762714A3A107071BD0CF06A135FAE29BFBBC92B037592DE060"
)

type File struct {
	Role   string
	Name   string
	Path   string
	Size   int64
	SHA256 string
}

type Assets struct {
	BSS File
	MSS File
}

type fileContract struct {
	role   string
	name   string
	size   int64
	sha256 string
}

type contracts struct {
	bss fileContract
	mss fileContract
}

type candidate struct {
	path string
	info os.FileInfo
}

func CheckAssets(bssPath, mssPath string) (Assets, error) {
	return checkAssets(bssPath, mssPath, contracts{
		bss: fileContract{role: "BSS", name: BSSName, size: BSSSize, sha256: BSSSHA256},
		mss: fileContract{role: "MSS", name: MSSName, size: MSSSize, sha256: MSSSHA256},
	})
}

func checkAssets(bssPath, mssPath string, expected contracts) (Assets, error) {
	bss, err := inspectCandidate(bssPath, expected.bss.role)
	if err != nil {
		return Assets{}, err
	}
	mss, err := inspectCandidate(mssPath, expected.mss.role)
	if err != nil {
		return Assets{}, err
	}
	if os.SameFile(bss.info, mss.info) {
		return Assets{}, errors.New("debug-capture BSS and MSS firmware must be different files")
	}

	bssFile, err := verifyCandidate(bss, expected.bss)
	if err != nil {
		return Assets{}, err
	}
	mssFile, err := verifyCandidate(mss, expected.mss)
	if err != nil {
		return Assets{}, err
	}
	return Assets{BSS: bssFile, MSS: mssFile}, nil
}

func inspectCandidate(path, role string) (candidate, error) {
	cleaned := strings.Trim(strings.TrimSpace(path), `"`)
	if cleaned == "" {
		return candidate{}, fmt.Errorf("debug-capture %s firmware path is empty", role)
	}
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return candidate{}, fmt.Errorf("resolve debug-capture %s firmware %q: %w", role, cleaned, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return candidate{}, fmt.Errorf("stat debug-capture %s firmware %s: %w", role, absolute, err)
	}
	if !info.Mode().IsRegular() {
		return candidate{}, fmt.Errorf("debug-capture %s firmware is not a regular file: %s", role, absolute)
	}
	return candidate{path: absolute, info: info}, nil
}

func verifyCandidate(candidate candidate, expected fileContract) (File, error) {
	if candidate.info.Size() != expected.size {
		return File{}, fmt.Errorf(
			"debug-capture %s firmware size mismatch: expected=%d actual=%d path=%s",
			expected.role,
			expected.size,
			candidate.info.Size(),
			candidate.path,
		)
	}
	digest, err := fileSHA256(candidate.path)
	if err != nil {
		return File{}, fmt.Errorf("hash debug-capture %s firmware %s: %w", expected.role, candidate.path, err)
	}
	if !strings.EqualFold(digest, expected.sha256) {
		return File{}, fmt.Errorf(
			"debug-capture %s firmware SHA-256 mismatch: expected=%s actual=%s path=%s",
			expected.role,
			expected.sha256,
			digest,
			candidate.path,
		)
	}
	return File{
		Role:   expected.role,
		Name:   expected.name,
		Path:   candidate.path,
		Size:   candidate.info.Size(),
		SHA256: digest,
	}, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

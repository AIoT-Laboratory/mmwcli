package iwr6843

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
	xwr68xxBSSName   = "xwr68xx_radarss.bin"
	xwr68xxBSSSize   = int64(240072)
	xwr68xxBSSSHA256 = "E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F"

	xwr68xxMSSName   = "xwr68xx_masterss.bin"
	xwr68xxMSSSize   = int64(92992)
	xwr68xxMSSSHA256 = "316911D4A8DBA1762714A3A107071BD0CF06A135FAE29BFBBC92B037592DE060"
)

type File struct {
	Role        string
	Name        string
	Path        string
	Size        int64
	SHA256      string
	EntryPoint  uint32
	RPRCVersion uint32
	Sections    int
	Writes      int

	image     rprcImage
	writePlan []memoryWrite
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
	target rprcTarget
}

type contracts struct {
	bss fileContract
	mss fileContract
}

type candidate struct {
	path string
	info os.FileInfo
	file *os.File
}

// CheckAssets pins both IWR6843 firmware images to their exact identities.
func CheckAssets(bssPath, mssPath string) (Assets, error) {
	return verifyAssets(bssPath, mssPath, iwr6843Assets)
}

func verifyAssets(bssPath, mssPath string, expected contracts) (Assets, error) {
	bss, err := inspectCandidate(bssPath, expected.bss.role)
	if err != nil {
		return Assets{}, err
	}
	defer bss.file.Close()
	mss, err := inspectCandidate(mssPath, expected.mss.role)
	if err != nil {
		return Assets{}, err
	}
	defer mss.file.Close()
	if os.SameFile(bss.info, mss.info) {
		return Assets{}, errors.New("IWR6843 BSS and MSS firmware must be different files")
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
		return candidate{}, fmt.Errorf("IWR6843 %s firmware path is empty", role)
	}
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return candidate{}, fmt.Errorf("resolve IWR6843 %s firmware %q: %w", role, cleaned, err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return candidate{}, fmt.Errorf("open IWR6843 %s firmware %s: %w", role, absolute, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return candidate{}, fmt.Errorf("stat IWR6843 %s firmware %s: %w", role, absolute, err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return candidate{}, fmt.Errorf("IWR6843 %s firmware is not a regular file: %s", role, absolute)
	}
	return candidate{path: absolute, info: info, file: file}, nil
}

func verifyCandidate(candidate candidate, expected fileContract) (File, error) {
	if candidate.info.Size() != expected.size {
		return File{}, fmt.Errorf(
			"IWR6843 %s firmware size mismatch: expected=%d actual=%d path=%s",
			expected.role,
			expected.size,
			candidate.info.Size(),
			candidate.path,
		)
	}
	content, err := io.ReadAll(io.LimitReader(candidate.file, expected.size+1))
	if err != nil {
		return File{}, fmt.Errorf("read IWR6843 %s firmware %s: %w", expected.role, candidate.path, err)
	}
	if int64(len(content)) != expected.size {
		return File{}, fmt.Errorf(
			"IWR6843 %s firmware changed while reading: expected=%d actual=%d path=%s",
			expected.role,
			expected.size,
			len(content),
			candidate.path,
		)
	}
	hash := sha256.Sum256(content)
	digest := strings.ToUpper(hex.EncodeToString(hash[:]))
	if !strings.EqualFold(digest, expected.sha256) {
		return File{}, fmt.Errorf(
			"IWR6843 %s firmware SHA-256 mismatch: expected=%s actual=%s path=%s",
			expected.role,
			expected.sha256,
			digest,
			candidate.path,
		)
	}
	image, err := parseRPRC(content)
	if err != nil {
		return File{}, fmt.Errorf("parse IWR6843 %s firmware RPRC %s: %w", expected.role, candidate.path, err)
	}
	writes, err := planMemoryWrites(image, expected.target)
	if err != nil {
		return File{}, fmt.Errorf("plan IWR6843 %s firmware writes %s: %w", expected.role, candidate.path, err)
	}
	return File{
		Role:        expected.role,
		Name:        expected.name,
		Path:        candidate.path,
		Size:        candidate.info.Size(),
		SHA256:      digest,
		EntryPoint:  image.entryPoints[0],
		RPRCVersion: image.version,
		Sections:    len(image.sections),
		Writes:      len(writes),
		image:       image,
		writePlan:   writes,
	}, nil
}

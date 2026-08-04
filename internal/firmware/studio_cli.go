package firmware

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
	StudioCLIName   = "mmwave_Studio_cli_xwr68xx.bin"
	StudioCLISize   = int64(358660)
	StudioCLISHA256 = "24BDAE9662AA8E611DBEDAE65709B7589CDCFB6E3F71B8E0E7FA78C5DD4A18BF"
)

type Info struct {
	Path   string
	Size   int64
	SHA256 string
}

type expectation struct {
	size   int64
	sha256 string
}

func VerifyStudioCLI(path string) (Info, error) {
	return verifyFile(path, expectation{
		size:   StudioCLISize,
		sha256: StudioCLISHA256,
	})
}

func verifyFile(path string, expected expectation) (Info, error) {
	cleaned := strings.Trim(strings.TrimSpace(path), `"`)
	if cleaned == "" {
		return Info{}, errors.New("TI studio_cli firmware path is empty")
	}
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return Info{}, fmt.Errorf("resolve TI studio_cli firmware %q: %w", cleaned, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Info{}, fmt.Errorf("stat TI studio_cli firmware %s: %w", absolute, err)
	}
	if !info.Mode().IsRegular() {
		return Info{}, fmt.Errorf("TI studio_cli firmware is not a regular file: %s", absolute)
	}
	if info.Size() != expected.size {
		return Info{}, fmt.Errorf(
			"TI studio_cli firmware size mismatch: expected=%d actual=%d path=%s",
			expected.size,
			info.Size(),
			absolute,
		)
	}
	digest, err := fileSHA256(absolute)
	if err != nil {
		return Info{}, fmt.Errorf("hash TI studio_cli firmware %s: %w", absolute, err)
	}
	if !strings.EqualFold(digest, expected.sha256) {
		return Info{}, fmt.Errorf(
			"TI studio_cli firmware SHA-256 mismatch: expected=%s actual=%s path=%s",
			expected.sha256,
			digest,
			absolute,
		)
	}
	return Info{Path: absolute, Size: info.Size(), SHA256: digest}, nil
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

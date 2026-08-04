package toolbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const SupportedVersion = "4.00.00.05"

type Installation struct {
	RootDirectory      string
	StudioCLIDirectory string
	MetadataPath       string
	PackageID          string
	PackageVersion     string
	FirmwarePath       string
	ProfilePath        string
	ManifestPath       string
}

type packageMetadata struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type assetExpectation struct {
	name   string
	path   func(Installation) string
	size   int64
	sha256 string
}

var knownAssets = []assetExpectation{
	{
		name:   "xWR68xx firmware",
		path:   func(i Installation) string { return i.FirmwarePath },
		size:   358660,
		sha256: "24BDAE9662AA8E611DBEDAE65709B7589CDCFB6E3F71B8E0E7FA78C5DD4A18BF",
	},
	{
		name:   "xWR68xx profile",
		path:   func(i Installation) string { return i.ProfilePath },
		size:   1409,
		sha256: "169C070C3F7E18D9E851BB272D13D6CC4E5211A2C9302BBBF91D5B3393D6A35A",
	},
	{
		name:   "Radar Toolbox manifest",
		path:   func(i Installation) string { return i.ManifestPath },
		size:   179989,
		sha256: "5683D43FB3A272DA3CB16F0FC8E1795F50751401D6544F956205F101AD0091D4",
	},
}

func Locate(explicitPath string) (Installation, error) {
	if strings.TrimSpace(explicitPath) != "" {
		return loadCandidate(explicitPath, "explicit Radar Toolbox path")
	}
	for _, name := range []string{"MMWCLI_RADAR_TOOLBOX_ROOT", "RADAR_TOOLBOX_ROOT"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return loadCandidate(value, name)
		}
	}

	var parents []string
	if runtime.GOOS == "windows" {
		parents = []string{`C:\ti`, `D:\Apps\ti`, `D:\App\ti`}
	} else {
		parents = []string{`/opt/ti`, `/usr/local/ti`}
		if home, err := os.UserHomeDir(); err == nil {
			parents = append(parents, filepath.Join(home, "ti"))
		}
	}

	var candidates []string
	for _, parent := range parents {
		matches, _ := filepath.Glob(filepath.Join(parent, "radar_toolbox_*"))
		candidates = append(candidates, matches...)
	}
	sort.Slice(candidates, func(a, b int) bool {
		return versionKey(candidates[a]) > versionKey(candidates[b])
	})
	if len(candidates) == 0 {
		return Installation{}, errors.New("Radar Toolbox not found; pass --toolbox-root or set MMWCLI_RADAR_TOOLBOX_ROOT")
	}
	return selectAutoCandidate(candidates)
}

func selectAutoCandidate(candidates []string) (Installation, error) {
	var failures []string
	for _, candidate := range candidates {
		installation, err := loadCandidate(candidate, "auto-discovered Radar Toolbox")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		if installation.PackageVersion != SupportedVersion {
			failures = append(failures, fmt.Sprintf(
				"%s: unsupported version %s",
				candidate,
				installation.PackageVersion,
			))
			continue
		}
		return installation, nil
	}
	return Installation{}, fmt.Errorf(
		"no usable Radar Toolbox %s installation was auto-discovered:\n  - %s",
		SupportedVersion,
		strings.Join(failures, "\n  - "),
	)
}

func loadCandidate(path, source string) (Installation, error) {
	root, err := normalizeRoot(path)
	if err != nil {
		return Installation{}, fmt.Errorf("%s: %w", source, err)
	}
	studioCLI := filepath.Join(root, "tools", "studio_cli")
	if info, err := os.Stat(studioCLI); err != nil || !info.IsDir() {
		return Installation{}, fmt.Errorf("%s is not a complete Radar Toolbox installation: missing %s", source, studioCLI)
	}
	metadataPath := filepath.Join(root, ".metadata", ".tirex", "package.tirex.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return Installation{}, fmt.Errorf("read Radar Toolbox metadata %s: %w", metadataPath, err)
	}
	var metadata []packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Installation{}, fmt.Errorf("parse Radar Toolbox metadata %s: %w", metadataPath, err)
	}
	if len(metadata) == 0 || metadata[0].ID == "" || metadata[0].Version == "" {
		return Installation{}, fmt.Errorf("Radar Toolbox metadata has no id/version: %s", metadataPath)
	}
	if metadata[0].ID != "radar_toolbox" {
		return Installation{}, fmt.Errorf("unexpected package id %q in %s", metadata[0].ID, metadataPath)
	}

	return Installation{
		RootDirectory:      root,
		StudioCLIDirectory: studioCLI,
		MetadataPath:       metadataPath,
		PackageID:          metadata[0].ID,
		PackageVersion:     metadata[0].Version,
		FirmwarePath:       filepath.Join(studioCLI, "prebuilt_binaries", "mmwave_Studio_cli_xwr68xx.bin"),
		ProfilePath:        filepath.Join(studioCLI, "src", "profiles", "profile_monitor_xwr68xx.cfg"),
		ManifestPath:       filepath.Join(root, "toolbox_docs", "RADAR_TOOLBOX_manifest.html"),
	}, nil
}

func normalizeRoot(path string) (string, error) {
	value := strings.Trim(strings.TrimSpace(path), `"`)
	if value == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", value, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	if strings.EqualFold(filepath.Base(abs), "studio_cli") && strings.EqualFold(filepath.Base(filepath.Dir(abs)), "tools") {
		return filepath.Dir(filepath.Dir(abs)), nil
	}
	return abs, nil
}

func versionKey(path string) string {
	name := strings.TrimPrefix(strings.ToLower(filepath.Base(path)), "radar_toolbox_")
	parts := strings.Split(name, "_")
	for index, part := range parts {
		if value, err := strconv.Atoi(part); err == nil {
			parts[index] = fmt.Sprintf("%08d", value)
		} else {
			parts[index] = part
		}
	}
	return strings.Join(parts, ".")
}

func Verify(installation Installation) error {
	if installation.PackageID != "radar_toolbox" {
		return fmt.Errorf("unsupported Radar Toolbox package id: %s", installation.PackageID)
	}
	if installation.PackageVersion != SupportedVersion {
		return fmt.Errorf("unsupported Radar Toolbox version %s; expected %s", installation.PackageVersion, SupportedVersion)
	}
	var failures []string
	for _, asset := range knownAssets {
		path := asset.path(installation)
		info, err := os.Stat(path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s missing: %s", asset.name, path))
			continue
		}
		if info.Size() != asset.size {
			failures = append(failures, fmt.Sprintf("%s size mismatch: expected=%d actual=%d path=%s", asset.name, asset.size, info.Size(), path))
			continue
		}
		digest, err := fileSHA256(path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s SHA-256 failed: %v", asset.name, err))
			continue
		}
		if !strings.EqualFold(digest, asset.sha256) {
			failures = append(failures, fmt.Sprintf("%s SHA-256 mismatch: expected=%s actual=%s path=%s", asset.name, asset.sha256, digest, path))
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("Radar Toolbox %s verification failed:\n  - %s", SupportedVersion, strings.Join(failures, "\n  - "))
	}
	return nil
}

func Print(w io.Writer, installation Installation) {
	fmt.Fprintln(w, "Radar Toolbox:")
	fmt.Fprintln(w, "  root:", installation.RootDirectory)
	fmt.Fprintf(w, "  package: %s %s\n", installation.PackageID, installation.PackageVersion)
	fmt.Fprintln(w, "  studio_cli:", installation.StudioCLIDirectory)
	fmt.Fprintln(w, "  firmware:", installation.FirmwarePath)
	fmt.Fprintln(w, "  profile:", installation.ProfilePath)
	fmt.Fprintln(w, "  manifest:", installation.ManifestPath)
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

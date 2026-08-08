package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testVersion = "0.2-test"

func TestDownloaderReadsExactReleaseAndVerifiesChecksum(t *testing.T) {
	assetName, command := platformDownloader(t)
	binary := []byte("verified mmwcli test binary\n")
	server := releaseServer(t, assetName, binary, false)
	installDir := t.TempDir()
	target := filepath.Join(installDir, "mmwcli")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	command = append(command, versionArguments()...)
	command = append(command, installDirectoryArguments(installDir)...)
	runDownloader(t, server.URL, command, true)

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Fatalf("installed bytes = %q, want %q", got, binary)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed Linux mode = %v, err = %v", info.Mode(), err)
		}
	}
}

func TestDownloaderRejectsChecksumMismatch(t *testing.T) {
	assetName, command := platformDownloader(t)
	server := releaseServer(t, assetName, []byte("tampered"), true)
	installDir := t.TempDir()
	command = append(command, installDirectoryArguments(installDir)...)
	output := runDownloader(t, server.URL, command, false)
	if !strings.Contains(output, "SHA-256 mismatch") {
		t.Fatalf("failure output does not explain checksum mismatch:\n%s", output)
	}
}

func platformDownloader(t *testing.T) (string, []string) {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	scriptsDir := filepath.Dir(current)
	architecture := "amd64"
	if runtime.GOARCH == "arm64" {
		architecture = "arm64"
	}
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf("mmwcli-%s-windows-%s.exe", testVersion, architecture),
			[]string{"pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join(scriptsDir, "download-mmwcli.ps1")}
	case "linux":
		return fmt.Sprintf("mmwcli-%s-linux-%s", testVersion, architecture),
			[]string{"sh", filepath.Join(scriptsDir, "download-mmwcli.sh")}
	default:
		t.Skip("downloaders target Windows and Linux")
		return "", nil
	}
}

func versionArguments() []string {
	if runtime.GOOS == "windows" {
		return []string{"-Version", testVersion}
	}
	return []string{"--version", testVersion}
}

func installDirectoryArguments(directory string) []string {
	if runtime.GOOS == "windows" {
		return []string{"-InstallDir", directory}
	}
	return []string{"--install-dir", directory}
}

func runDownloader(t *testing.T, baseURL string, arguments []string, wantSuccess bool) string {
	t.Helper()
	command := exec.Command(arguments[0], arguments[1:]...)
	command.Env = append(os.Environ(),
		"MMWCLI_RELEASE_API_BASE="+baseURL,
		"MMWCLI_RELEASE_DOWNLOAD_BASE="+baseURL+"/download",
	)
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("downloader failed: %v\n%s", err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("downloader unexpectedly succeeded:\n%s", output)
	}
	return string(output)
}

func releaseServer(t *testing.T, assetName string, binary []byte, mismatch bool) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(binary)
	if mismatch {
		digest = sha256.Sum256([]byte("different"))
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases/latest", "/releases/tags/" + testVersion:
			assets := []map[string]string{
				{"name": assetName, "browser_download_url": server.URL + "/download/" + testVersion + "/" + assetName},
				{"name": "SHA256SUMS", "browser_download_url": server.URL + "/download/" + testVersion + "/SHA256SUMS"},
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"tag_name": testVersion, "assets": assets})
		case "/download/" + testVersion + "/" + assetName:
			_, _ = writer.Write(binary)
		case "/download/" + testVersion + "/SHA256SUMS":
			_, _ = fmt.Fprintf(writer, "%s  %s\n", strings.ToUpper(hex.EncodeToString(digest[:])), assetName)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

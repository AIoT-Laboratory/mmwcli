package toolbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateExplicitRootAndStudioCLIDirectory(t *testing.T) {
	root := t.TempDir()
	studioCLI := filepath.Join(root, "tools", "studio_cli")
	metadataDirectory := filepath.Join(root, ".metadata", ".tirex")
	if err := os.MkdirAll(studioCLI, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(metadataDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `[{"id":"radar_toolbox","version":"4.00.00.05"}]`
	if err := os.WriteFile(filepath.Join(metadataDirectory, "package.tirex.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, candidate := range []string{root, studioCLI} {
		installation, err := Locate(candidate)
		if err != nil {
			t.Fatalf("Locate(%q): %v", candidate, err)
		}
		if installation.PackageVersion != SupportedVersion {
			t.Fatalf("version = %q", installation.PackageVersion)
		}
		if installation.RootDirectory != root {
			t.Fatalf("root = %q, want %q", installation.RootDirectory, root)
		}
	}
}

func TestLocateRejectsWrongPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools", "studio_cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadataDirectory := filepath.Join(root, ".metadata", ".tirex")
	if err := os.MkdirAll(metadataDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(metadataDirectory, "package.tirex.json"),
		[]byte(`[{"id":"not_radar_toolbox","version":"4.00.00.05"}]`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Locate(root); err == nil {
		t.Fatal("Locate accepted wrong package id")
	}
}

func TestAutoDiscoveryFallsBackToSupportedInstallation(t *testing.T) {
	parent := t.TempDir()
	newer := makeToolboxCandidate(t, parent, "radar_toolbox_5_00_00_00", "5.00.00.00")
	supported := makeToolboxCandidate(t, parent, "radar_toolbox_4_00_00_05", SupportedVersion)
	installation, err := selectAutoCandidate([]string{newer, supported})
	if err != nil {
		t.Fatal(err)
	}
	if installation.RootDirectory != supported {
		t.Fatalf("selected root = %q, want supported fallback %q", installation.RootDirectory, supported)
	}
}

func makeToolboxCandidate(t *testing.T, parent, name, version string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(root, "tools", "studio_cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadataDirectory := filepath.Join(root, ".metadata", ".tirex")
	if err := os.MkdirAll(metadataDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `[{"id":"radar_toolbox","version":"` + version + `"}]`
	if err := os.WriteFile(filepath.Join(metadataDirectory, "package.tirex.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

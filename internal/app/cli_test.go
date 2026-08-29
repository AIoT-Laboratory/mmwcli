package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicCLIExposesCameraCommands(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	help := stdout.String()
	for _, command := range []string{
		"mmwcli check", "mmwcli capture", "mmwcli camera list", "mmwcli camera preview", "mmwcli version",
	} {
		if !strings.Contains(help, command) {
			t.Fatalf("help missing %q", command)
		}
	}
	for _, removed := range []string{"debug-cli", "studio-cli", "multisensor", "sensor-producer", "repl", "doctor"} {
		if strings.Contains(help, removed) {
			t.Fatalf("help still exposes %q", removed)
		}
		stdout.Reset()
		stderr.Reset()
		if code := Run([]string{removed}, &stdout, &stderr); code != 2 {
			t.Fatalf("removed command %q exit = %d", removed, code)
		}
	}
}

func TestCaptureFlagsRequireFiniteFramesAndRig(t *testing.T) {
	for _, arguments := range [][]string{
		{"capture", "radar.cfg", "take"},
		{"capture", "radar.cfg", "take", "--rig", "rig.json", "--frames", "0"},
		{"check", "radar.cfg", "--rig", "rig.json", "--frames", "65536"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("Run(%v) exit = %d, stderr=%s", arguments, code, stderr.String())
		}
	}
}

func TestLoadRigResolvesFirmwareRelativeToRig(t *testing.T) {
	root := t.TempDir()
	rigPath := filepath.Join(root, "rig.json")
	encoded := `{
  "schema": "mmwcli.rig.v2",
  "port": "COM3",
  "bss": "firmware/bss.bin",
  "mss": "firmware/mss.bin",
  "d2xx": "AR-DevPack-EVM-012",
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "height_m": 1.5
}`
	if err := os.WriteFile(rigPath, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
	rig, err := loadRig(rigPath, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if rig.BSS != filepath.Join(root, "firmware", "bss.bin") ||
		rig.MSS != filepath.Join(root, "firmware", "mss.bin") {
		t.Fatalf("relative firmware paths not resolved: %+v", rig)
	}
	if _, err := loadRig(rigPath, false, ""); err == nil || !strings.Contains(err.Error(), "camera") {
		t.Fatalf("camera requirement error = %v", err)
	}
}

func TestLoadRigUsesStructuredCameraAndOverride(t *testing.T) {
	root := t.TempDir()
	rigPath := filepath.Join(root, "rig.json")
	encoded := `{
  "schema": "mmwcli.rig.v2",
  "port": "COM3",
  "bss": "bss.bin",
  "mss": "mss.bin",
  "d2xx": "AR-DevPack-EVM-012",
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "camera": {"device": "Default Camera", "width": 1280, "height": 720, "fps": 30, "max_bytes": 2097152},
  "height_m": 1.5
}`
	if err := os.WriteFile(rigPath, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
	rig, err := loadRig(rigPath, false, "@device_pnp_camera")
	if err != nil {
		t.Fatal(err)
	}
	if rig.Camera == nil || rig.Camera.Device != "@device_pnp_camera" || rig.Camera.Width != 1280 {
		t.Fatalf("camera override = %#v", rig.Camera)
	}
}

func TestCaptureFlagsRejectCameraWithRadarOnly(t *testing.T) {
	for _, camera := range []string{"id", "   "} {
		_, err := parseCaptureOptions(
			"capture", "capture", []string{"radar.cfg", "take", "--rig", "rig.json", "--frames", "1", "--radar-only", "--camera", camera}, true, io.Discard,
		)
		if err == nil || !strings.Contains(err.Error(), "--camera cannot") {
			t.Fatalf("camera/radar-only error for %q = %v", camera, err)
		}
	}
}

func TestCameraPreviewUsesCameraFlag(t *testing.T) {
	rig, camera, err := parseCameraFlags(
		"camera preview", "preview", []string{"--rig", "rig.json", "--camera", "camera-id"}, true, io.Discard,
	)
	if err != nil || rig != "rig.json" || camera != "camera-id" {
		t.Fatalf("preview options = %q, %q, %v", rig, camera, err)
	}
}

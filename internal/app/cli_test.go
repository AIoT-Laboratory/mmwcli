package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicCLIStaysSmall(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	help := stdout.String()
	for _, command := range []string{
		"mmwcli check", "mmwcli capture", "mmwcli stream", "mmwcli camera list", "mmwcli camera preview", "mmwcli version",
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

func TestStreamFlagsExposeOnlyConfigAndRig(t *testing.T) {
	options, err := parseStreamOptions([]string{"radar.cfg", "--rig", "rig.json"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.configPath != "radar.cfg" || options.rigPath != "rig.json" {
		t.Fatalf("stream options = %+v", options)
	}
	for _, arguments := range [][]string{
		{"radar.cfg"},
		{"radar.cfg", "--rig", "rig.json", "--frames", "1"},
		{"radar.cfg", "--rig", "rig.json", "--camera", "id"},
	} {
		if _, err := parseStreamOptions(arguments, io.Discard); err == nil {
			t.Fatalf("stream options accepted %v", arguments)
		}
	}
}

func TestStreamHeaderIsOneExactJSONLine(t *testing.T) {
	var output bytes.Buffer
	header := streamHeader{FrameBytes: 4, PeriodNS: 10, HeightM: 1.5, TiltDeg: 90}
	if err := json.NewEncoder(&output).Encode(header); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"frame_bytes\":4,\"period_ns\":10,\"height_m\":1.5,\"tilt_deg\":90}\n"; got != want {
		t.Fatalf("stream header = %q, want %q", got, want)
	}
}

func TestStreamControlCancelsOnStopOrEOF(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		t.Cleanup(func() {
			_ = reader.Close()
			_ = writer.Close()
		})
		go watchStreamStop(reader, cancel)

		if _, err := io.WriteString(writer, "unknown\n"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("unknown stream control cancelled capture")
		default:
		}
		if _, err := io.WriteString(writer, "stop\n"); err != nil {
			t.Fatal(err)
		}
		waitForCancellation(t, ctx)
	})

	t.Run("EOF", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		defer reader.Close()
		go watchStreamStop(reader, cancel)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		waitForCancellation(t, ctx)
	})
}

func waitForCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream control did not cancel capture")
	}
}

func TestLoadRigResolvesFirmwareRelativeToRig(t *testing.T) {
	root := t.TempDir()
	rigPath := filepath.Join(root, "rig.json")
	encoded := `{
  "schema": "mmwcli.rig.v3",
  "port": "COM3",
  "bss": "firmware/bss.bin",
  "mss": "firmware/mss.bin",
  "d2xx": "AR-DevPack-EVM-012",
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "height_m": 1.5,
  "tilt_deg": 90
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
  "schema": "mmwcli.rig.v3",
  "port": "COM3",
  "bss": "bss.bin",
  "mss": "mss.bin",
  "d2xx": "AR-DevPack-EVM-012",
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "camera": {"device": "Default Camera", "width": 1280, "height": 720, "fps": 30, "max_bytes": 2097152},
  "height_m": 1.5,
  "tilt_deg": 90
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

func TestRigRequiresVersionThreeWithNinetyDegreeTilt(t *testing.T) {
	root := t.TempDir()
	base := `{
  "schema": %q,
  "port": "COM3",
  "bss": "bss.bin",
  "mss": "mss.bin",
  "d2xx": "AR-DevPack-EVM-012",
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "height_m": 1.5,
  "tilt_deg": %v
}`
	for index, test := range []struct {
		schema string
		tilt   any
	}{
		{schema: "mmwcli.rig.v2", tilt: 90},
		{schema: "mmwcli.rig.v3", tilt: 0},
		{schema: "mmwcli.rig.v3", tilt: 89.5},
	} {
		path := filepath.Join(root, fmt.Sprintf("rig-%d.json", index))
		if err := os.WriteFile(path, []byte(fmt.Sprintf(base, test.schema, test.tilt)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRig(path, true, ""); err == nil {
			t.Fatalf("loadRig accepted schema=%s tilt=%v", test.schema, test.tilt)
		}
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

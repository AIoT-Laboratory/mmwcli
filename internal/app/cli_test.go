package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		"mmwcli setup show", "mmwcli setup roi", "mmwcli probe", "mmwcli check", "mmwcli capture", "mmwcli stream", "mmwcli camera list", "mmwcli camera preview", "mmwcli version",
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

func TestCaptureFlagsRequireFiniteFramesAndSetup(t *testing.T) {
	for _, arguments := range [][]string{
		{"capture", "radar.cfg", "take.capture"},
		{"capture", "radar.cfg", "take.capture", "--setup", "setup.json", "--frames", "0"},
		{"check", "radar.cfg", "--setup", "setup.json", "--frames", "65536"},
		{"check", "radar.cfg", "--rig", "old.json", "--frames", "1", "--radar-only"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("Run(%v) exit = %d, stderr=%s", arguments, code, stderr.String())
		}
	}
}

func TestStreamFlagsExposeOnlyConfigAndSetup(t *testing.T) {
	options, err := parseStreamOptions([]string{"radar.cfg", "--setup", "setup.json"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.configPath != "radar.cfg" || options.setupPath != "setup.json" {
		t.Fatalf("stream options = %+v", options)
	}
	for _, arguments := range [][]string{
		{"radar.cfg"},
		{"radar.cfg", "--setup", "setup.json", "--frames", "1"},
		{"radar.cfg", "--setup", "setup.json", "--camera", "id"},
	} {
		if _, err := parseStreamOptions(arguments, io.Discard); err == nil {
			t.Fatalf("stream options accepted %v", arguments)
		}
	}
}

func TestStreamHeaderIsOneExactJSONLine(t *testing.T) {
	var output bytes.Buffer
	header := streamHeader{FrameBytes: 4, PeriodNS: 10, Mount: setupMount{HeightM: 1.5, PitchDeg: 90}}
	if err := json.NewEncoder(&output).Encode(header); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"frame_bytes\":4,\"period_ns\":10,\"mount\":{\"height_m\":1.5,\"pitch_deg\":90}}\n"; got != want {
		t.Fatalf("stream header = %q, want %q", got, want)
	}
}

func TestStreamHeaderIncludesConfiguredROI(t *testing.T) {
	var output bytes.Buffer
	roi := setupROI{
		Frame: levelROIFrame,
		MinM:  [3]float64{0.5, -1.5, 0},
		MaxM:  [3]float64{5.5, 1.5, 2.2},
	}
	header := streamHeader{
		FrameBytes: 4, PeriodNS: 10,
		Mount: setupMount{HeightM: 1.5, PitchDeg: 90}, ROI: &roi,
	}
	if err := json.NewEncoder(&output).Encode(header); err != nil {
		t.Fatal(err)
	}
	want := "{\"frame_bytes\":4,\"period_ns\":10,\"mount\":{\"height_m\":1.5,\"pitch_deg\":90},\"roi\":{\"frame\":\"level_forward_lateral_up\",\"min_m\":[0.5,-1.5,0],\"max_m\":[5.5,1.5,2.2]}}\n"
	if got := output.String(); got != want {
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

func TestManagedCaptureControlRequiresExactStopLineOrEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	go watchManagedStop(reader, cancel)
	for _, ignored := range []string{"stop\r\n", " stop\n", "unknown\n"} {
		if _, err := io.WriteString(writer, ignored); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("managed control accepted %q", ignored)
		default:
		}
	}
	if _, err := io.WriteString(writer, "stop\n"); err != nil {
		t.Fatal(err)
	}
	waitForCancellation(t, ctx)
}

func TestCaptureCancellationExits130OnlyAfterCleanCameraShutdown(t *testing.T) {
	var stderr bytes.Buffer
	cleanCancellation := errors.Join(
		context.Canceled,
		fmt.Errorf("finish capture participant: %w", context.Canceled),
	)
	if code := commandExitCode("capture", cleanCancellation, &stderr); code != 130 {
		t.Fatalf("clean cancellation exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cleanup completed") {
		t.Fatalf("clean cancellation message = %q", stderr.String())
	}

	stderr.Reset()
	cameraCloseErr := errors.New("camera shutdown failed")
	failedCleanup := errors.Join(context.Canceled, cameraCloseErr)
	if code := commandExitCode("capture", failedCleanup, &stderr); code != 4 {
		t.Fatalf("camera cleanup failure exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), cameraCloseErr.Error()) {
		t.Fatalf("camera cleanup failure was hidden: %q", stderr.String())
	}
}

func waitForCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream control did not cancel capture")
	}
}

func TestLoadSetupResolvesFirmwareRelativeToSetup(t *testing.T) {
	root := t.TempDir()
	setupPath := filepath.Join(root, "setup.json")
	encoded := `{
  "schema": "mmwcli.setup.v1",
  "radar": {"port": "COM3", "bss": "firmware/bss.bin", "mss": "firmware/mss.bin", "d2xx": "AR-DevPack-EVM-012"},
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "mount": {"height_m": 1.5}
}`
	if err := os.WriteFile(setupPath, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
	setup, err := loadSetup(setupPath)
	if err != nil {
		t.Fatal(err)
	}
	if setup.bssPath != filepath.Join(root, "firmware", "bss.bin") ||
		setup.mssPath != filepath.Join(root, "firmware", "mss.bin") {
		t.Fatalf("relative firmware paths not resolved: %+v", setup)
	}
	if setup.Mount.PitchDeg != 90 {
		t.Fatalf("default mount pitch = %v", setup.Mount.PitchDeg)
	}
	if setup.ROI != nil {
		t.Fatalf("legacy setup gained ROI: %+v", setup.ROI)
	}
	if _, err := setup.cameraConfig("camera", false); err == nil || !strings.Contains(err.Error(), "camera") {
		t.Fatalf("camera requirement error = %v", err)
	}
}

func TestSetupROIUpdatesPhysicalBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	writeTestSetup(t, path, false)
	arguments := []string{
		"setup", "roi", path,
		"--min-forward", "0.5", "--max-forward", "6.5",
		"--min-lateral", "-2", "--max-lateral", "2",
		"--min-up", "0", "--max-up", "2.5",
	}
	if code := Run(arguments, io.Discard, io.Discard); code != 0 {
		t.Fatalf("setup roi exit = %d", code)
	}
	setup, err := loadSetup(path)
	if err != nil {
		t.Fatal(err)
	}
	want := setupROI{
		Frame: levelROIFrame,
		MinM:  [3]float64{0.5, -2, 0},
		MaxM:  [3]float64{6.5, 2, 2.5},
	}
	if setup.ROI == nil || *setup.ROI != want {
		t.Fatalf("updated ROI = %+v, want %+v", setup.ROI, want)
	}
}

func TestSetupROIRejectsInvalidBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	writeTestSetup(t, path, false)
	for _, arguments := range [][]string{
		{"setup", "roi", path},
		{"setup", "roi", path, "--min-forward", "-0.1", "--max-forward", "5", "--min-lateral", "-1", "--max-lateral", "1", "--min-up", "0", "--max-up", "2"},
		{"setup", "roi", path, "--min-forward", "5", "--max-forward", "5", "--min-lateral", "-1", "--max-lateral", "1", "--min-up", "0", "--max-up", "2"},
		{"setup", "roi", path, "--min-forward", "0.5", "--max-forward", "5", "--min-lateral", "-1", "--max-lateral", "1", "--min-up", "-0.1", "--max-up", "2"},
		{"setup", "roi", path, "--min-forward", "NaN", "--max-forward", "5", "--min-lateral", "-1", "--max-lateral", "1", "--min-up", "0", "--max-up", "2"},
	} {
		if code := Run(arguments, io.Discard, io.Discard); code != 2 {
			t.Fatalf("Run(%v) exit = %d", arguments, code)
		}
	}
	setup, err := loadSetup(path)
	if err != nil || setup.ROI != nil {
		t.Fatalf("invalid update changed setup ROI: %+v, %v", setup.ROI, err)
	}
}

func TestValidateROIRequiresExactLevelFrame(t *testing.T) {
	valid := setupROI{
		Frame: levelROIFrame,
		MinM:  [3]float64{0.5, -1.5, 0},
		MaxM:  [3]float64{5.5, 1.5, 2.2},
	}
	if err := validateROI(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Frame = "sensor_xyz"
	if err := validateROI(invalid); err == nil {
		t.Fatal("ROI with the wrong coordinate frame was accepted")
	}
}

func TestSetupROIVectorsRequireThreeValues(t *testing.T) {
	for _, encoded := range []string{"[0, 1]", "[0, 1, 2, 3]"} {
		var vector setupVector
		if err := json.Unmarshal([]byte(encoded), &vector); err == nil {
			t.Fatalf("setup ROI vector accepted %s", encoded)
		}
	}
}

func TestTrackedSetupExampleHasResearchROI(t *testing.T) {
	setup, err := loadSetup(filepath.Join("..", "..", "hardware", "setup.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := setupROI{
		Frame: levelROIFrame,
		MinM:  [3]float64{0.5, -1.5, 0},
		MaxM:  [3]float64{5.5, 1.5, 2.2},
	}
	if setup.ROI == nil || *setup.ROI != want {
		t.Fatalf("tracked setup ROI = %+v, want %+v", setup.ROI, want)
	}
}

func TestSetupShowAndMountUseOneStrictSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	writeTestSetup(t, path, false)
	var stdout bytes.Buffer
	if code := Run([]string{"setup", "show", path}, &stdout, io.Discard); code != 0 {
		t.Fatalf("setup show exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "\n  \"radar\": {") || strings.Contains(stdout.String(), "bssPath") {
		t.Fatalf("setup show is not normalized JSON: %s", stdout.String())
	}
	if code := Run(
		[]string{"setup", "mount", path, "--height", "1.75", "--pitch", "90"},
		io.Discard, io.Discard,
	); code != 0 {
		t.Fatalf("setup mount exit = %d", code)
	}
	setup, err := loadSetup(path)
	if err != nil || setup.Mount.HeightM != 1.75 || setup.Mount.PitchDeg != 90 {
		t.Fatalf("updated mount = %+v, %v", setup.Mount, err)
	}
	for _, pitch := range []string{"1", "0.5"} {
		if code := Run(
			[]string{"setup", "mount", path, "--height", "1.75", "--pitch", pitch},
			io.Discard, io.Discard,
		); code != 2 {
			t.Fatalf("pitch %s exit = %d", pitch, code)
		}
	}
	if code := Run(
		[]string{"setup", "mount", path, "--height", "NaN", "--pitch", "90"},
		io.Discard, io.Discard,
	); code != 2 {
		t.Fatalf("NaN height exit = %d", code)
	}
}

func TestLoadSetupUsesCameraFormatAndSelectedDevice(t *testing.T) {
	root := t.TempDir()
	setupPath := filepath.Join(root, "setup.json")
	encoded := `{
  "schema": "mmwcli.setup.v1",
  "radar": {"port": "COM3", "bss": "bss.bin", "mss": "mss.bin", "d2xx": "AR-DevPack-EVM-012"},
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "mount": {"height_m": 1.5, "pitch_deg": 90},
  "camera": {"width": 1280, "height": 720, "fps": 30, "max_bytes": 2097152}
}`
	if err := os.WriteFile(setupPath, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
	setup, err := loadSetup(setupPath)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := setup.cameraConfig("@device_pnp_camera", false)
	if err != nil || configured.Device != "@device_pnp_camera" || configured.Width != 1280 {
		t.Fatalf("camera config = %#v, %v", configured, err)
	}
}

func writeTestSetup(t *testing.T, path string, camera bool) {
	t.Helper()
	cameraJSON := ""
	if camera {
		cameraJSON = `,
  "camera": {"width": 1280, "height": 720, "fps": 30, "max_bytes": 2097152}`
	}
	encoded := fmt.Sprintf(`{
  "schema": "mmwcli.setup.v1",
  "radar": {"port": "COM3", "bss": "bss.bin", "mss": "mss.bin", "d2xx": "AR-DevPack-EVM-012"},
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "mount": {"height_m": 1.5, "pitch_deg": 90}%s
}`, cameraJSON)
	if err := os.WriteFile(path, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSetupRequiresVersionOneAndDiscretePitch(t *testing.T) {
	root := t.TempDir()
	base := `{
  "schema": %q,
  "radar": {"port": "COM3", "bss": "bss.bin", "mss": "mss.bin", "d2xx": "AR-DevPack-EVM-012"},
  "dca": {"host": "192.168.33.30", "device": "192.168.33.180", "delay_us": 50},
  "mount": {"height_m": 1.5, "pitch_deg": %v}
}`
	for index, test := range []struct {
		schema string
		pitch  any
		valid  bool
	}{
		{schema: "mmwcli.setup.v0", pitch: 90},
		{schema: "mmwcli.setup.v1", pitch: 90, valid: true},
		{schema: "mmwcli.setup.v1", pitch: 0, valid: true},
		{schema: "mmwcli.setup.v1", pitch: 1},
		{schema: "mmwcli.setup.v1", pitch: 0.5},
	} {
		path := filepath.Join(root, fmt.Sprintf("setup-%d.json", index))
		if err := os.WriteFile(path, []byte(fmt.Sprintf(base, test.schema, test.pitch)), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadSetup(path)
		if (err == nil) != test.valid {
			t.Fatalf("loadSetup schema=%s pitch=%v error = %v", test.schema, test.pitch, err)
		}
	}
}

func TestSetupRejectsStoredCameraDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	writeTestSetup(t, path, true)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte(`"camera": {`), []byte(`"camera": {"device":"old",`), 1)
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSetup(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("stored camera device error = %v", err)
	}
}

func TestCaptureFlagsRejectCameraWithRadarOnly(t *testing.T) {
	for _, camera := range []string{"id", "   "} {
		_, err := parseCaptureOptions(
			"capture", "capture", []string{"radar.cfg", "take.capture", "--setup", "setup.json", "--frames", "1", "--radar-only", "--camera", camera}, true, io.Discard,
		)
		if err == nil || !strings.Contains(err.Error(), "--camera cannot") {
			t.Fatalf("camera/radar-only error for %q = %v", camera, err)
		}
	}
}

func TestCameraPreviewUsesCameraFlag(t *testing.T) {
	setup, camera, err := parseCameraPreview([]string{"--setup", "setup.json", "--camera", "camera-id"}, io.Discard)
	if err != nil || setup != "setup.json" || camera != "camera-id" {
		t.Fatalf("preview = %q, %q, %v", setup, camera, err)
	}
}

func TestCaptureOutputOwnsOnePartSuffix(t *testing.T) {
	valid, err := parseCaptureOptions(
		"capture", "capture",
		[]string{"radar.cfg", "take.capture", "--setup", "setup.json", "--frames", "1", "--radar-only"},
		true, io.Discard,
	)
	if err != nil || valid.outputPath != "take.capture" {
		t.Fatalf("valid output = %+v, %v", valid, err)
	}
	for _, output := range []string{"take", "take.capture.part"} {
		_, err := parseCaptureOptions(
			"capture", "capture",
			[]string{"radar.cfg", output, "--setup", "setup.json", "--frames", "1", "--radar-only"},
			true, io.Discard,
		)
		if err == nil {
			t.Fatalf("capture accepted output %q", output)
		}
	}
}

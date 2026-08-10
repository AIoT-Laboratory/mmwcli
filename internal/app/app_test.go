package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mmwcli/internal/d2xx"
	"mmwcli/internal/dca"
	"mmwcli/internal/debugcapture"
	"mmwcli/internal/radar"
	"mmwcli/internal/session"
)

func TestHelpContainsOnlyCLIBackends(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	text := stdout.String()
	const productIdentity = "mmwcli dev - TI xWR16xx/xWR18xx/xWR68xx cross-platform CLI control\n\n"
	if !strings.HasPrefix(text, productIdentity) {
		t.Fatalf("help does not report the development identity:\n%s", text)
	}
	for _, expected := range []string{"mmwcli version", "studio-cli", "repl", "dca", "debug-cli", "native-check", "cross-platform"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("help does not contain %q:\n%s", expected, text)
		}
	}
	if !strings.Contains(text, "[--sop2-reset] [options]") {
		t.Fatalf("top-level debug-cli synopsis omits capture options:\n%s", text)
	}
	for _, removed := range []string{"mmwcli demo", "studio lua", "--studio-root", "--legacy-studio", ".NET"} {
		if strings.Contains(text, removed) {
			t.Fatalf("help still contains removed backend %q:\n%s", removed, text)
		}
	}
}

func TestVersionReportsDevelopmentIdentity(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if Version != "dev" || !strings.HasPrefix(stdout.String(), "mmwcli dev (") {
		t.Fatalf("development version output = %q", stdout.String())
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	for _, command := range []string{"studio", "demo", "debug-capture"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run([]string{command}, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "unknown command: "+command) {
				t.Fatalf("missing unknown command error: %s", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("unknown command wrote stdout: %q", stdout.String())
			}
		})
	}
}

func TestStudioCLICheckIsOffline(t *testing.T) {
	config := writeValidConfig(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"studio-cli", "check", config}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "commands=9") || !strings.Contains(stdout.String(), "frames=1") {
		t.Fatalf("unexpected check output: %s", stdout.String())
	}
}

func TestStudioCLIRejectsRadarFamilyFlag(t *testing.T) {
	root := t.TempDir()
	missingConfig := filepath.Join(root, "missing.cfg")
	missingPort := "__mmwcli_test_missing_port__"
	output := filepath.Join(root, "capture.bin")
	for _, arguments := range [][]string{
		{"studio-cli", "check", missingConfig, "--radar-family", "xwr18xx"},
		{"studio-cli", "version", "--radar-family", "xwr18xx", "--port", missingPort},
		{"studio-cli", "capture", missingConfig, output, "--radar-family", "xwr18xx", "--port", missingPort},
	} {
		t.Run(arguments[1], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "flag provided but not defined: -radar-family") {
				t.Fatalf("missing rejected flag error: %s", stderr.String())
			}
		})
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestCaptureRequiresPortBeforeCreatingPart(t *testing.T) {
	config := writeValidConfig(t)
	output := filepath.Join(t.TempDir(), "capture.bin")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"studio-cli", "capture", config, output}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	for _, path := range []string{output, output + ".part"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("offline argument failure created %s: %v", path, err)
		}
	}
}

func TestDCAInvalidAddressFailsBeforeDial(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"dca", "ping", "--device", "not-an-ip"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
}

func TestDCAStandaloneCaptureIsRemoved(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.bin")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"dca", "capture", output}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown dca command: capture") {
		t.Fatalf("missing removed-command error: %s", stderr.String())
	}
	for _, path := range []string{output, output + ".part"} {
		assertPathDoesNotExist(t, path)
	}
}

func TestCaptureRejectsUnsupportedDCAFPGAConfigurationBeforeOutputOrHardware(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
	}{
		{
			name: "integrated SD mode",
			arguments: []string{
				"studio-cli", "capture", "CONFIG", "OUTPUT", "--port", "COM3", "--capture-mode", "1",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "capture.bin")
			config := writeValidConfig(t)
			arguments := append([]string(nil), test.arguments...)
			for index, argument := range arguments {
				switch argument {
				case "OUTPUT":
					arguments[index] = output
				case "CONFIG":
					arguments[index] = config
				}
			}
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "xWR68xx raw capture requires") {
				t.Fatalf("missing capture-mode contract: %s", stderr.String())
			}
			for _, path := range []string{output, output + ".part"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("offline mode failure created %s: %v", path, err)
				}
			}
		})
	}
}

func TestCommandHelpDoesNotRequirePositionalsOrHardware(t *testing.T) {
	commands := [][]string{
		{"studio-cli", "--help"},
		{"studio-cli", "check", "--help"},
		{"studio-cli", "apply", "--help"},
		{"studio-cli", "capture", "--help"},
		{"dca", "--help"},
		{"firmware", "--help"},
		{"debug-cli", "--help"},
		{"debug-cli", "check", "--help"},
		{"doctor", "--help"},
	}
	for _, arguments := range commands {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestStudioCaptureRejectsRemovedNoReconfigureFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"studio-cli", "capture", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "no-reconfig") {
		t.Fatalf("studio-cli capture help retains no-reconfig: %s", stderr.String())
	}

	config := writeValidConfig(t)
	output := filepath.Join(t.TempDir(), "capture.bin")
	stdout.Reset()
	stderr.Reset()
	arguments := []string{"studio-cli", "capture", config, output, "--port", "COM3", "--no-reconfig"}
	if code := Run(arguments, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -no-reconfig") {
		t.Fatalf("missing removed flag error: %s", stderr.String())
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestCaptureCommandsRejectRemovedSessionDirectoryFlag(t *testing.T) {
	for _, arguments := range [][]string{
		{"studio-cli", "capture", "--help"},
		{"debug-cli", "capture", "--help"},
		{"studio-cli", "check", "--help"},
	} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			help := stdout.String() + stderr.String()
			if strings.Contains(help, "session-dir") {
				t.Fatalf("help retains removed session-dir flag:\n%s", help)
			}
		})
	}

	output := filepath.Join(t.TempDir(), "capture-session")
	for _, arguments := range [][]string{
		{"studio-cli", "capture", "CONFIG", output, "--session-dir"},
		{"debug-cli", "capture", "CONFIG", output, "--session-dir"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("arguments %v: exit code = %d, stdout=%s stderr=%s", arguments, code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "flag provided but not defined: -session-dir") {
			t.Fatalf("arguments %v: missing removed flag error: %s", arguments, stderr.String())
		}
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestStudioCaptureSessionContractPrecedesOutput(t *testing.T) {
	config := writeValidConfig(t)
	contents, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "adcCfg 2 1", "adcCfg 2 2", 1))
	if err := os.WriteFile(config, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "capture-session")
	var stdout, stderr bytes.Buffer
	arguments := []string{"studio-cli", "capture", config, output, "--port", "COM3"}
	if code := Run(arguments, &stdout, &stderr); code != 4 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "exact adcCfg 2 1") {
		t.Fatalf("missing session contract error: %s", stderr.String())
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestStudioCaptureSessionReservationPrecedesHardware(t *testing.T) {
	config := writeValidConfig(t)
	output := filepath.Join(t.TempDir(), "capture-session")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(output, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{"studio-cli", "capture", config, output, "--port", "COM3"}
	if code := Run(arguments, &stdout, &stderr); code != 4 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("missing output reservation error: %s", stderr.String())
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing output changed: %q, %v", contents, err)
	}
	assertPathDoesNotExist(t, output+".part")
}

func TestStudioCaptureSessionOutputPublishesDirectory(t *testing.T) {
	config := writeValidConfig(t)
	configSnapshot, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := loadCaptureOutputPlan(radar.StudioCLI, config)
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "capture-session")
	output, err := createCaptureOutput(outputPath, prepared.finalizeSession)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	adc := make([]byte, int(prepared.plan.ExpectedBytes))
	if written, err := output.WriteAt(adc, 0); err != nil || written != len(adc) {
		t.Fatalf("write ADC = %d, %v", written, err)
	}
	if err := output.Truncate(int64(len(adc))); err != nil {
		t.Fatal(err)
	}
	if err := output.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if got := strings.Join(names, ","); got != "adc.bin,capture.json,radar.cfg" {
		t.Fatalf("session files = %s", got)
	}
	archivedConfig, err := os.ReadFile(filepath.Join(outputPath, "radar.cfg"))
	if err != nil || !bytes.Equal(archivedConfig, configSnapshot) {
		t.Fatalf("archived config differs from snapshot: %v", err)
	}
	assertPathDoesNotExist(t, outputPath+".part")
}

func TestReadCaptureSessionConfigRejectsOversizeInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversize.cfg")
	if err := os.WriteFile(path, make([]byte, captureSessionMaxConfigBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCaptureSessionConfig(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize config error = %v", err)
	}
}

func TestDoctorHelpUsesCommandSynopsis(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	help := stderr.String()
	if !strings.Contains(help, "usage: mmwcli doctor [--studio-cli-firmware FILE]") {
		t.Fatalf("doctor help missing command synopsis:\n%s", help)
	}
	if strings.Contains(help, "Usage of doctor:") {
		t.Fatalf("doctor help used default FlagSet synopsis:\n%s", help)
	}
}

func TestDoctorDoesNotRequireFirmware(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "TI studio_cli firmware: not checked") {
		t.Fatalf("doctor implied a firmware dependency:\n%s", stdout.String())
	}
}

func TestFirmwareVerifyRequiresExplicitFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"firmware", "verify"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "exactly one FILE") {
		t.Fatalf("missing explicit-file error: %s", stderr.String())
	}
}

func TestDebugCaptureCheckRequiresExplicitAssets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"debug-cli", "check", "--family", "xwr68xx", "--bss-fw", "bss.bin"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires --bss-fw FILE and --mss-fw FILE") {
		t.Fatalf("missing explicit-assets error: %s", stderr.String())
	}
}

func TestDebugCaptureCheckRejectsBadAssetsOffline(t *testing.T) {
	root := t.TempDir()
	bssPath := filepath.Join(root, "bss.bin")
	mssPath := filepath.Join(root, "mss.bin")
	for _, path := range []string{bssPath, mssPath} {
		if err := os.WriteFile(path, []byte("not TI firmware"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{
		"debug-cli", "check", "--family", "xwr68xx",
		"--bss-fw", bssPath, "--mss-fw", mssPath,
	}
	if code := Run(arguments, &stdout, &stderr); code != 4 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "BSS firmware size mismatch") {
		t.Fatalf("missing asset error: %s", stderr.String())
	}
}

func TestDebugCaptureCommandsRequireExactFamilyBeforePreflight(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "capture-session")
	tests := []struct {
		name      string
		arguments []string
		match     string
	}{
		{
			name:      "check missing",
			arguments: []string{"debug-cli", "check", "--bss-fw", "missing-bss", "--mss-fw", "missing-mss"},
			match:     "--family is required",
		},
		{
			name:      "check model alias",
			arguments: []string{"debug-cli", "check", "--family", "iwr6843", "--bss-fw", "missing-bss", "--mss-fw", "missing-mss"},
			match:     "xwr16xx, xwr18xx, or xwr68xx",
		},
		{
			name: "capture missing",
			arguments: []string{
				"debug-cli", "capture", "missing.cfg", output,
				"--enhanced-port", "COM3", "--bss-fw", "missing-bss", "--mss-fw", "missing-mss",
				"--d2xx-serial", "FT1234",
			},
			match: "--family is required",
		},
		{
			name: "capture model alias",
			arguments: []string{
				"debug-cli", "capture", "missing.cfg", output, "--family", "iwr6843",
				"--enhanced-port", "COM3", "--bss-fw", "missing-bss", "--mss-fw", "missing-mss",
				"--d2xx-serial", "FT1234",
			},
			match: "xwr16xx, xwr18xx, or xwr68xx",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(test.arguments, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), test.match) {
				t.Fatalf("stderr = %s, want %q", stderr.String(), test.match)
			}
			if stdout.Len() != 0 {
				t.Fatalf("family preflight wrote stdout: %q", stdout.String())
			}
			assertPathDoesNotExist(t, output)
			assertPathDoesNotExist(t, output+".part")
		})
	}
}

func TestDebugCaptureNativeCheckUsesLibraryOnly(t *testing.T) {
	library := &fakeNativeD2XXLibrary{info: d2xx.Info{
		Library:      "fake-ftd2xx",
		Version:      0x00030214,
		VersionKnown: true,
	}}
	var stdout, stderr bytes.Buffer
	err := runDebugCaptureNativeCheck(nil, &stdout, &stderr, func() (nativeD2XXLibrary, error) {
		return library, nil
	})
	if err != nil {
		t.Fatalf("native check: %v", err)
	}
	if library.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", library.closeCalls)
	}
	for _, expected := range []string{
		"FTDI D2XX library: fake-ftd2xx",
		"FTDI D2XX version: 3.2.14 (0x00030214)",
		"library only; no hardware accessed",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

type fakeNativeD2XXLibrary struct {
	info       d2xx.Info
	closeCalls int
}

func (library *fakeNativeD2XXLibrary) Info() d2xx.Info {
	return library.info
}

func (library *fakeNativeD2XXLibrary) Close() error {
	library.closeCalls++
	return nil
}

func TestPrintDebugCaptureAssetIncludesRPRCWritePlan(t *testing.T) {
	var output bytes.Buffer
	printDebugCaptureAsset(&output, debugcapture.File{
		Role:        "BSS",
		Name:        "xwr68xx_radarss.bin",
		Path:        "bss.bin",
		Size:        240072,
		SHA256:      "E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F",
		RPRCVersion: 1,
		Sections:    12,
		Writes:      65,
	})
	if !strings.Contains(output.String(), "RPRC: version=1 entry=0x00000000 sections=12 write-chunks=65") {
		t.Fatalf("missing RPRC write plan: %s", output.String())
	}
}

func TestActionSpecificFlagsFailBeforeHardware(t *testing.T) {
	for _, arguments := range [][]string{
		{"dca", "start", "--reset"},
		{"studio-cli", "stop", "--no-reconfig", "--port", "COM3"},
	} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestIntegratedDataFormatMismatchFailsBeforePartOrHardware(t *testing.T) {
	config := writeValidConfig(t)
	output := filepath.Join(t.TempDir(), "capture.bin")
	var stdout, stderr bytes.Buffer
	arguments := []string{"studio-cli", "capture", config, output, "--port", "COM3", "--data-format", "2"}
	if code := Run(arguments, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	for _, path := range []string{output, output + ".part"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("offline mismatch created %s: %v", path, err)
		}
	}
}

func TestIntegratedCaptureRaisesRawTailGuardBeforeOutputOrHardware(t *testing.T) {
	config := writeValidConfig(t)
	output := filepath.Join(t.TempDir(), "already-exists.bin")
	if err := os.WriteFile(output, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{
		"studio-cli", "capture", config, output,
		"--port", "COM3", "--idle-ms", "100",
	}
	if code := Run(arguments, &stdout, &stderr); code != 4 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "raised from 100ms to 2.5s") {
		t.Fatalf("missing raw tail guard adjustment: %s", stdout.String())
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: %q, %v", data, err)
	}
}

func TestIntegratedInfiniteCaptureRequiresGracefulStopControl(t *testing.T) {
	config := filepath.Join(t.TempDir(), "small-frames.cfg")
	content := strings.Join([]string{
		"flushCfg",
		"dfeDataOutputMode 1",
		"channelCfg 1 1 0",
		"adcCfg 2 1",
		"adcbufCfg -1 0 1 1 1",
		"profileCfg 0 60 7 3 40 0 0 100 1 16 5000 0 0 30",
		"chirpCfg 0 0 0 0 0 0 0 1",
		"frameCfg 0 0 1 0 1342 1 0",
		"lvdsStreamCfg -1 0 1 0",
		"sensorStart",
	}, "\n")
	if err := os.WriteFile(config, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "already-exists.bin")
	if err := os.WriteFile(output, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	arguments := []string{"studio-cli", "capture", config, output, "--port", "COM3"}
	if code := Run(arguments, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires --stop-on-stdin-eof") {
		t.Fatalf("missing graceful-stop requirement: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("rejected infinite capture wrote stdout: %s", stdout.String())
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: %q, %v", data, err)
	}

	stdout.Reset()
	stderr.Reset()
	arguments = append(arguments, "--stop-on-stdin-eof")
	if code := Run(arguments, &stdout, &stderr); code != 4 {
		t.Fatalf("controlled infinite capture exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("controlled infinite capture did not reach output reservation: %s", stderr.String())
	}
}

func TestCancellationCleanupClassification(t *testing.T) {
	if !cancellationOnly(context.Canceled) {
		t.Fatal("plain cancellation was not classified as cancellation-only")
	}
	convergence := &dca.StartConvergenceError{
		StartErr: context.Canceled,
		StopErr:  errors.New("stop timeout"),
	}
	if cancellationOnly(convergence) {
		t.Fatal("failed cleanup was classified as cancellation-only")
	}
	convergedCancellation := &dca.StartConvergenceError{StartErr: context.Canceled}
	if !cancellationOnly(convergedCancellation) {
		t.Fatal("cancellation with successful stop convergence was not classified as cancellation-only")
	}
	nonCleanupConvergence := &dca.StartConvergenceError{StartErr: context.Canceled}
	joined := errors.Join(
		nonCleanupConvergence,
		&session.CleanupError{Err: errors.New("radar stop timeout")},
	)
	if cancellationOnly(joined) {
		t.Fatal("joined cleanup failure was classified as cancellation-only")
	}
	if cancellationOnly(errors.Join(context.Canceled, errors.New("malformed DCA packet"))) {
		t.Fatal("operational failure joined with cancellation was hidden as clean cancellation")
	}
}

func TestREPLCancellationClassification(t *testing.T) {
	if !replInvocationCancelled([]string{"REPL"}, context.Canceled) {
		t.Fatal("repl cancellation was not classified for exit 130")
	}
	if replInvocationCancelled([]string{"studio-cli", "capture"}, context.Canceled) {
		t.Fatal("non-repl cancellation was classified as repl")
	}
	if replInvocationCancelled(
		[]string{"repl"},
		errors.Join(context.Canceled, errors.New("close failed")),
	) {
		t.Fatal("repl cancellation with a close failure was classified as clean cancellation")
	}
}

func TestOnlyCaptureInvocationsClaimCancellationCleanup(t *testing.T) {
	for _, arguments := range [][]string{
		{"studio-cli", "CAPTURE"},
		{"DEBUG-CLI", "CAPTURE"},
	} {
		if !captureInvocationHasCleanup(arguments) {
			t.Fatalf("capture invocation was not cleanup eligible: %v", arguments)
		}
	}
	for _, arguments := range [][]string{
		{"studio-cli", "start"},
		{"debug-capture", "capture"},
		{"demo", "capture"},
		{"dca", "capture"},
		{"dca", "stop"},
		{"dca", "version"},
		{"firmware", "verify"},
		{"capture"},
	} {
		if captureInvocationHasCleanup(arguments) {
			t.Fatalf("non-capture invocation claimed cleanup eligibility: %v", arguments)
		}
	}
}

func writeValidConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw.cfg")
	content := strings.Join([]string{
		"flushCfg",
		"dfeDataOutputMode 1",
		"channelCfg 15 7 0",
		"adcCfg 2 1",
		"adcbufCfg -1 0 1 1 1",
		"profileCfg 0 60 7 3 40 0 0 100 1 256 5000 0 0 30",
		"chirpCfg 0 0 0 0 0 0 0 1",
		"frameCfg 0 0 1 1 10 1 0",
		"lvdsStreamCfg -1 0 1 0",
		"sensorStart",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

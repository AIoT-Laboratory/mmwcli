package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/d2xx"
	"mmwcli/internal/dca"
	"mmwcli/internal/debugcapture"
	"mmwcli/internal/radar"
	"mmwcli/internal/session"
)

const debugCaptureTestConfig = `
flushCfg
dfeDataOutputMode 1
channelCfg 15 7 0
adcCfg 2 1
adcbufCfg -1 0 1 1 1
profileCfg 0 60 7 3 24 0 0 166 1 256 12500 0 0 158
chirpCfg 0 0 0 0 0 0 0 1
chirpCfg 1 1 0 0 0 0 0 4
frameCfg 0 1 32 100 100 1 0
lowPower 0 0
lvdsStreamCfg -1 0 1 0
`

func TestLoadDebugCaptureOutputPlanAcceptsInfiniteFrames(t *testing.T) {
	config := writeDebugCaptureConfig(t, strings.Replace(
		debugCaptureTestConfig,
		"frameCfg 0 1 32 100 100 1 0",
		"frameCfg 0 1 32 0 100 1 0",
		1,
	))
	family, err := radar.ParseDeviceFamily("xwr68xx")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := loadDebugCaptureOutputPlan(family, config)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.plan.InfiniteFrames || prepared.plan.NumberOfFrames != 0 ||
		prepared.plan.ExpectedBytes != 0 {
		t.Fatalf("infinite debug capture plan = %+v", prepared.plan)
	}
}

func TestDebugCaptureInfiniteRequiresGracefulStopControlWithoutHardware(t *testing.T) {
	config := writeDebugCaptureConfig(t, strings.Replace(
		debugCaptureTestConfig,
		"frameCfg 0 1 32 100 100 1 0",
		"frameCfg 0 1 32 0 100 1 0",
		1,
	))
	output := filepath.Join(t.TempDir(), "capture-session")
	var hardwareCalls int
	dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)

	err := runDebugCaptureCaptureWithDependencies(
		debugCaptureArguments(config, output),
		io.Discard,
		io.Discard,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "requires --stop-on-stdin-eof") {
		t.Fatalf("infinite debug capture error = %v", err)
	}
	if hardwareCalls != 0 {
		t.Fatalf("infinite preflight made %d hardware call(s)", hardwareCalls)
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestDebugCaptureFiniteRejectsGracefulStopControlWithoutHardware(t *testing.T) {
	config := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	output := filepath.Join(t.TempDir(), "capture-session")
	var hardwareCalls int
	dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)

	err := runDebugCaptureCaptureWithDependencies(
		append(debugCaptureArguments(config, output), "--stop-on-stdin-eof"),
		io.Discard,
		io.Discard,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "valid only when the effective frame count is 0") {
		t.Fatalf("finite debug capture error = %v", err)
	}
	if hardwareCalls != 0 {
		t.Fatalf("finite preflight made %d hardware call(s)", hardwareCalls)
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestDebugCaptureCapturePreflightFailureDoesNotCreatePartOrTouchHardware(t *testing.T) {
	validConfig := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	invalidConfig := writeDebugCaptureConfig(t, "sensorStart\n")
	unrepresentableConfig := writeDebugCaptureConfig(
		t,
		strings.Replace(debugCaptureTestConfig, "adcCfg 2 1", "adcCfg 2 2", 1),
	)

	tests := []struct {
		name      string
		config    string
		extraArgs []string
		match     string
	}{
		{
			name:   "invalid CFG",
			config: invalidConfig,
			extraArgs: []string{
				"--enhanced-port", "COM3", "--bss-fw", "bss.bin", "--mss-fw", "mss.bin",
				"--d2xx-serial", "FT1234",
			},
		},
		{
			name:   "session contract",
			config: unrepresentableConfig,
			extraArgs: []string{
				"--enhanced-port", "COM3", "--bss-fw", "bss.bin", "--mss-fw", "mss.bin",
				"--d2xx-serial", "FT1234",
			},
			match: "exact adcCfg 2 1",
		},
		{
			name:   "both selectors",
			config: validConfig,
			extraArgs: []string{
				"--enhanced-port", "COM3", "--bss-fw", "bss.bin", "--mss-fw", "mss.bin",
				"--d2xx-serial", "FT1234", "--d2xx-description", "FTDI Device",
			},
		},
		{
			name:   "non raw DCA mode",
			config: validConfig,
			extraArgs: []string{
				"--enhanced-port", "COM3", "--bss-fw", "bss.bin", "--mss-fw", "mss.bin",
				"--d2xx-serial", "FT1234", "--data-format", "2",
			},
		},
		{
			name:   "finite bound too small",
			config: validConfig,
			extraArgs: []string{
				"--enhanced-port", "COM3", "--bss-fw", "bss.bin", "--mss-fw", "mss.bin",
				"--d2xx-serial", "FT1234", "--max-bytes", "1024",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture.bin")
			var hardwareCalls int
			dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)
			arguments := append(
				[]string{test.config, output, "--family", "xwr68xx"},
				test.extraArgs...,
			)
			err := runDebugCaptureCaptureWithDependencies(
				arguments,
				io.Discard,
				io.Discard,
				dependencies,
			)
			if err == nil {
				t.Fatal("preflight unexpectedly passed")
			}
			if test.match != "" && !strings.Contains(err.Error(), test.match) {
				t.Fatalf("preflight error = %v, want %q", err, test.match)
			}
			if hardwareCalls != 0 {
				t.Fatalf("preflight made %d hardware call(s)", hardwareCalls)
			}
			assertPathDoesNotExist(t, output)
			assertPathDoesNotExist(t, output+".part")
		})
	}
}

func TestDebugCaptureCaptureBindsExplicitFamilyAcrossOfflinePlans(t *testing.T) {
	tests := []struct {
		family string
		config string
	}{
		{family: "xwr16xx", config: filepath.Join("..", "..", "hardware", "debug-cli-xwr16xx-raw.cfg")},
		{family: "xwr18xx", config: filepath.Join("..", "..", "hardware", "debug-cli-xwr18xx-raw.cfg")},
		{family: "xwr68xx", config: filepath.Join("..", "..", "hardware", "debug-cli-xwr6843-raw.cfg")},
	}

	for _, test := range tests {
		t.Run(test.family, func(t *testing.T) {
			device, err := radar.ParseDeviceFamily(test.family)
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "capture-session")
			wantErr := errors.New("stop after family-bound asset preflight")
			var calls []string
			var hardwareCalls int
			dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)
			dependencies.validateFPGA = func(got radar.DeviceFamily, config dca.FPGAConfig) error {
				calls = append(calls, "dca:"+got.Name())
				if got != device {
					t.Fatalf("DCA family = %s, want %s", got.Name(), device.Name())
				}
				return debugcapture.ValidateRawCaptureFPGAConfigForFamily(got, config)
			}
			dependencies.buildLinkPlan = func(
				got radar.DeviceFamily,
				plan radar.CapturePlan,
			) (debugcapture.Plan, error) {
				calls = append(calls, "link:"+got.Name())
				if got != device || plan.DeviceFamily() != device {
					t.Fatalf(
						"link family = %s, plan family = %s, want %s",
						got.Name(),
						plan.DeviceFamily().Name(),
						device.Name(),
					)
				}
				return debugcapture.BuildPlanForFamily(got, plan)
			}
			dependencies.checkAssets = func(
				got radar.DeviceFamily,
				_, _ string,
			) (debugcapture.Assets, error) {
				calls = append(calls, "assets:"+got.Name())
				if got != device {
					t.Fatalf("asset family = %s, want %s", got.Name(), device.Name())
				}
				return debugcapture.Assets{}, wantErr
			}

			err = runDebugCaptureCaptureWithDependencies(
				debugCaptureArgumentsForFamily(test.config, output, test.family),
				io.Discard,
				io.Discard,
				dependencies,
			)
			if !errors.Is(err, wantErr) {
				t.Fatalf("family preflight error = %v, want %v", err, wantErr)
			}
			wantCalls := []string{
				"dca:" + test.family,
				"link:" + test.family,
				"assets:" + test.family,
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("family calls = %v, want %v", calls, wantCalls)
			}
			if hardwareCalls != 0 {
				t.Fatalf("family preflight made %d hardware call(s)", hardwareCalls)
			}
			assertPathDoesNotExist(t, output)
			assertPathDoesNotExist(t, output+".part")
		})
	}
}

func TestDebugCaptureCaptureNativePreflightPrecedesOutputAndHardware(t *testing.T) {
	config := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	output := filepath.Join(t.TempDir(), "capture.bin")
	wantErr := errors.New("D2XX backend unavailable")
	var hardwareCalls int
	dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)
	dependencies.checkNative = func() error { return wantErr }

	err := runDebugCaptureCaptureWithDependencies(
		debugCaptureArguments(config, output),
		io.Discard,
		io.Discard,
		dependencies,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runDebugCaptureCaptureWithDependencies error = %v, want %v", err, wantErr)
	}
	if hardwareCalls != 0 {
		t.Fatalf("native preflight made %d hardware call(s)", hardwareCalls)
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestDebugCaptureCaptureOutputReservationPrecedesHardware(t *testing.T) {
	config := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	output := filepath.Join(t.TempDir(), "capture-session")
	if err := os.WriteFile(output, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	var hardwareCalls int
	dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)
	err := runDebugCaptureCaptureWithDependencies(
		debugCaptureArguments(config, output),
		io.Discard,
		io.Discard,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("output preflight error = %v", err)
	}
	if hardwareCalls != 0 {
		t.Fatalf("output preflight made %d hardware call(s)", hardwareCalls)
	}
	contents, err := os.ReadFile(output)
	if err != nil || string(contents) != "existing" {
		t.Fatalf("existing output changed: %q, %v", contents, err)
	}
	assertPathDoesNotExist(t, output+".part")
}

func TestDebugCaptureCaptureReportsJoinedPostCommitCloseFailures(t *testing.T) {
	config := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	outputPath := filepath.Join(t.TempDir(), "capture-session")
	var events []string
	dcaCloseCause := errors.New("late DCA close failure")
	controllerCloseCause := errors.New("late controller close failure")
	dcaClient := &fakeDebugCaptureDCA{events: &events, closeErr: dcaCloseCause}
	controller := &fakeDebugCaptureController{events: &events, closeErr: controllerCloseCause}
	dependencies := preflightOnlyDebugCaptureDependencies(nil)
	dependencies.checkAssets = func(radar.DeviceFamily, string, string) (debugcapture.Assets, error) {
		events = append(events, "assets")
		return debugcapture.Assets{}, nil
	}
	dependencies.checkNative = func() error {
		events = append(events, "native")
		return nil
	}
	dependencies.dialDCA = func(dca.Options) (debugCaptureDCA, error) {
		events = append(events, "dca")
		return dcaClient, nil
	}
	dependencies.openController = func(
		_ context.Context,
		options debugcapture.ControllerOptions,
	) (debugCaptureController, error) {
		events = append(events, "controller")
		if options.EnhancedPort != "COM3" {
			t.Fatalf("EnhancedPort = %q", options.EnhancedPort)
		}
		if !options.ResetSOP2 {
			t.Fatal("ResetSOP2 was not enabled by --sop2-reset")
		}
		if options.Selectors.SPI != (d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FT1234A"}) ||
			options.Selectors.IRQ != (d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FT1234B"}) {
			t.Fatalf("selectors = %+v", options.Selectors)
		}
		return controller, nil
	}
	dependencies.runSession = func(
		ctx context.Context,
		_ session.Radar,
		_ session.DCAControl,
		_ session.ReceiverFactory,
		plan radar.CapturePlan,
		output capturefile.Output,
		options session.Options,
	) (dca.CaptureStats, error) {
		events = append(events, "session")
		if options.ResetFPGA {
			t.Fatal("DCA reset was enabled without --reset")
		}
		if options.ReceiverConfig.MaxOutputBytes != plan.ExpectedBytes {
			t.Fatalf(
				"receiver maximum = %d, expected finite size %d",
				options.ReceiverConfig.MaxOutputBytes,
				plan.ExpectedBytes,
			)
		}
		if err := output.Truncate(plan.ExpectedBytes); err != nil {
			return dca.CaptureStats{}, err
		}
		if err := output.CommitContext(ctx); err != nil {
			return dca.CaptureStats{}, err
		}
		return dca.CaptureStats{OutputBytes: plan.ExpectedBytes}, nil
	}

	err := runDebugCaptureCaptureWithDependencies(
		append(debugCaptureArguments(config, outputPath), "--sop2-reset"),
		io.Discard,
		io.Discard,
		dependencies,
	)
	if !errors.Is(err, dcaCloseCause) || !errors.Is(err, controllerCloseCause) {
		t.Fatalf("post-commit error did not join both close causes: %v", err)
	}
	var cleanup *session.CleanupError
	if !errors.As(err, &cleanup) {
		t.Fatalf("post-commit close error is not marked as cleanup: %v", err)
	}
	if count := strings.Count(err.Error(), "capture cleanup failed: post-commit cleanup"); count != 2 {
		t.Fatalf("post-commit cleanup markers = %d, want 2: %v", count, err)
	}
	for _, resource := range []string{"debug-cli controller", "DCA1000 control client"} {
		if !strings.Contains(err.Error(), "close "+resource) {
			t.Fatalf("post-commit error lacks %s cause: %v", resource, err)
		}
	}
	wantEvents := []string{"assets", "native", "dca", "controller", "session", "controller-close", "dca-close"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
	if info, err := os.Stat(outputPath); err != nil || !info.IsDir() {
		t.Fatalf("published output: %v", err)
	}
	assertPathDoesNotExist(t, outputPath+".part")
}

func TestDebugCapturePublishesV1FromConfigSnapshot(t *testing.T) {
	sourceConfigContents := strings.Replace(
		debugCaptureTestConfig,
		"frameCfg 0 1 32 100 100 1 0",
		"frameCfg 0 1 1 1 10 1 0",
		1,
	)
	effectiveConfigContents := strings.Replace(
		sourceConfigContents,
		"frameCfg 0 1 1 1 10 1 0",
		"frameCfg 0 1 1 0 10 1 0",
		1,
	)
	config := writeDebugCaptureConfig(t, sourceConfigContents)
	outputPath := filepath.Join(t.TempDir(), "capture-session")
	var events []string
	dcaClient := &fakeDebugCaptureDCA{events: &events}
	controller := &fakeDebugCaptureController{events: &events}
	dependencies := preflightOnlyDebugCaptureDependencies(nil)
	dependencies.checkAssets = func(radar.DeviceFamily, string, string) (debugcapture.Assets, error) {
		events = append(events, "assets")
		if err := os.WriteFile(config, []byte("sensorStart\n"), 0o644); err != nil {
			return debugcapture.Assets{}, err
		}
		return debugcapture.Assets{}, nil
	}
	dependencies.checkNative = func() error {
		events = append(events, "native")
		return nil
	}
	dependencies.dialDCA = func(dca.Options) (debugCaptureDCA, error) {
		events = append(events, "dca")
		return dcaClient, nil
	}
	dependencies.openController = func(
		context.Context,
		debugcapture.ControllerOptions,
	) (debugCaptureController, error) {
		events = append(events, "controller")
		return controller, nil
	}
	var adcBytes []byte
	dependencies.runSession = func(
		ctx context.Context,
		_ session.Radar,
		_ session.DCAControl,
		_ session.ReceiverFactory,
		plan radar.CapturePlan,
		output capturefile.Output,
		options session.Options,
	) (dca.CaptureStats, error) {
		events = append(events, "session")
		if !plan.InfiniteFrames || plan.NumberOfFrames != 0 || plan.ExpectedBytes != 0 {
			t.Fatalf("session plan is not open-ended: %+v", plan)
		}
		if options.StopRequested == nil {
			t.Fatal("open-ended debug capture has no requested-stop control")
		}
		select {
		case <-options.StopRequested:
		case <-time.After(time.Second):
			t.Fatal("stdin EOF did not request a graceful stop")
		}
		adcBytes = make([]byte, int(plan.BytesPerFrame))
		for index := range adcBytes {
			adcBytes[index] = byte(index % 251)
		}
		written, err := output.WriteAt(adcBytes, 0)
		if err != nil {
			return dca.CaptureStats{}, err
		}
		if written != len(adcBytes) {
			return dca.CaptureStats{}, io.ErrShortWrite
		}
		if err := output.Truncate(int64(len(adcBytes))); err != nil {
			return dca.CaptureStats{}, err
		}
		if err := output.CommitContext(ctx); err != nil {
			return dca.CaptureStats{}, err
		}
		return dca.CaptureStats{OutputBytes: int64(len(adcBytes))}, nil
	}

	if err := runDebugCaptureCaptureWithDependencies(
		append(
			debugCaptureArguments(config, outputPath),
			"--frame-count", "0",
			"--stop-on-stdin-eof",
		),
		io.Discard,
		io.Discard,
		dependencies,
	); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"assets", "native", "dca", "controller", "session", "controller-close", "dca-close"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
	entries, err := os.ReadDir(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if want := []string{"adc.bin", "capture.json", "radar.cfg"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("session files = %v, want %v", names, want)
	}
	archivedConfig, err := os.ReadFile(filepath.Join(outputPath, "radar.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(archivedConfig) != effectiveConfigContents {
		t.Fatal("archived radar.cfg did not preserve the preflight snapshot")
	}
	archivedADC, err := os.ReadFile(filepath.Join(outputPath, "adc.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archivedADC, adcBytes) {
		t.Fatal("archived ADC bytes differ from captured bytes")
	}
	var manifest struct {
		Schema string `json:"schema"`
		ADC    struct {
			Path      string `json:"path"`
			Layout    string `json:"layout"`
			SizeBytes int64  `json:"size_bytes"`
		} `json:"adc"`
		RadarConfig struct {
			Path string `json:"path"`
		} `json:"radar_config"`
	}
	manifestBytes, err := os.ReadFile(filepath.Join(outputPath, "capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "mmwcli.capture_session.v1" ||
		manifest.ADC.Path != "adc.bin" || manifest.ADC.Layout != "group2_i_then_q" ||
		manifest.ADC.SizeBytes != int64(len(adcBytes)) ||
		manifest.RadarConfig.Path != "radar.cfg" {
		t.Fatalf("unexpected capture manifest: %+v", manifest)
	}
	assertPathDoesNotExist(t, outputPath+".part")
}

func TestDebugCaptureCaptureHelpHasNoTextCLIRouteFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"debug-cli", "capture", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d: %s", code, stderr.String())
	}
	help := stdout.String() + stderr.String()
	for _, expected := range []string{
		"--family", "xwr16xx", "xwr18xx", "xwr68xx", "-device string",
		"--enhanced-port", "--bss-fw", "--mss-fw", "--d2xx-serial",
		"--d2xx-description", "--sop2-reset", "-frame-count",
		"-stop-on-stdin-eof", "stream",
	} {
		if !strings.Contains(help, expected) {
			t.Errorf("help does not contain %s: %s", expected, help)
		}
	}
	for _, forbidden := range []string{"--no-reconfig", "session-dir", "--baud", "--serial-timeout-ms"} {
		if strings.Contains(help, forbidden) {
			t.Errorf("help contains forbidden text-CLI flag %s: %s", forbidden, help)
		}
	}
	if !captureInvocationHasCleanup([]string{"debug-cli", "capture"}) {
		t.Fatal("debug-cli capture was not classified for cancellation cleanup")
	}
}

func TestBuildDebugCaptureDescriptionSelectors(t *testing.T) {
	selectors, err := buildDebugCaptureSelectors("", "FTDI Device")
	if err != nil {
		t.Fatal(err)
	}
	if selectors.SPI != (d2xx.Selector{By: d2xx.SelectByDescription, Value: "FTDI Device A"}) ||
		selectors.IRQ != (d2xx.Selector{By: d2xx.SelectByDescription, Value: "FTDI Device B"}) {
		t.Fatalf("selectors = %+v", selectors)
	}
}

func writeDebugCaptureConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.cfg")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func debugCaptureArguments(config, output string) []string {
	return debugCaptureArgumentsForFamily(config, output, "xwr68xx")
}

func debugCaptureArgumentsForFamily(config, output, family string) []string {
	return []string{
		config,
		output,
		"--family", family,
		"--enhanced-port", "COM3",
		"--bss-fw", "bss.bin",
		"--mss-fw", "mss.bin",
		"--d2xx-serial", "FT1234",
	}
}

func preflightOnlyDebugCaptureDependencies(hardwareCalls *int) debugCaptureDependencies {
	return debugCaptureDependencies{
		stdin: strings.NewReader(""),
		checkAssets: func(radar.DeviceFamily, string, string) (debugcapture.Assets, error) {
			return debugcapture.Assets{}, nil
		},
		validateFPGA:  debugcapture.ValidateRawCaptureFPGAConfigForFamily,
		buildLinkPlan: debugcapture.BuildPlanForFamily,
		checkNative:   func() error { return nil },
		dialDCA: func(dca.Options) (debugCaptureDCA, error) {
			if hardwareCalls != nil {
				(*hardwareCalls)++
			}
			return nil, errors.New("unexpected DCA hardware access")
		},
		openController: func(context.Context, debugcapture.ControllerOptions) (debugCaptureController, error) {
			if hardwareCalls != nil {
				(*hardwareCalls)++
			}
			return nil, errors.New("unexpected radar hardware access")
		},
		newReceiver: func(dca.ReceiverConfig) (session.DataReceiver, error) {
			return nil, errors.New("unexpected receiver access")
		},
		runSession: func(
			context.Context,
			session.Radar,
			session.DCAControl,
			session.ReceiverFactory,
			radar.CapturePlan,
			capturefile.Output,
			session.Options,
		) (dca.CaptureStats, error) {
			return dca.CaptureStats{}, errors.New("unexpected session access")
		},
		context: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
	}
}

func assertPathDoesNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s exists or could not be checked: %v", path, err)
	}
}

type fakeDebugCaptureDCA struct {
	events   *[]string
	closeErr error
}

func (client *fakeDebugCaptureDCA) Execute(context.Context, dca.Command, []byte) (dca.Response, error) {
	panic("unexpected Execute")
}

func (client *fakeDebugCaptureDCA) Configure(context.Context, dca.FPGAConfig, int) (dca.ConfigurationResponses, error) {
	panic("unexpected Configure")
}

func (client *fakeDebugCaptureDCA) StartRecordConvergent(context.Context) (dca.Response, error) {
	panic("unexpected StartRecordConvergent")
}

func (client *fakeDebugCaptureDCA) StopRecord(context.Context) (dca.Response, error) {
	panic("unexpected StopRecord")
}

func (client *fakeDebugCaptureDCA) TakeAsyncStatuses() []dca.Response { return nil }

func (client *fakeDebugCaptureDCA) DrainAsyncStatuses(context.Context, time.Duration) ([]dca.Response, error) {
	panic("unexpected DrainAsyncStatuses")
}

func (client *fakeDebugCaptureDCA) Close() error {
	*client.events = append(*client.events, "dca-close")
	return client.closeErr
}

type fakeDebugCaptureController struct {
	events   *[]string
	closeErr error
}

func (controller *fakeDebugCaptureController) VerifyPlatformContext(context.Context) (string, error) {
	panic("unexpected VerifyPlatformContext")
}

func (controller *fakeDebugCaptureController) StopContext(context.Context) (string, error) {
	panic("unexpected StopContext")
}

func (controller *fakeDebugCaptureController) ApplyContext(context.Context, radar.CapturePlan) error {
	panic("unexpected ApplyContext")
}

func (controller *fakeDebugCaptureController) StartContext(context.Context) (string, error) {
	panic("unexpected StartContext")
}

func (controller *fakeDebugCaptureController) StartWithoutReconfigurationContext(context.Context) (string, error) {
	panic("unexpected StartWithoutReconfigurationContext")
}

func (controller *fakeDebugCaptureController) Close() error {
	*controller.events = append(*controller.events, "controller-close")
	return controller.closeErr
}

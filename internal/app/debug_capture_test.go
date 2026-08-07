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
				"--d2xx-serial", "FT1234", "--session-dir",
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
			arguments := append([]string{test.config, output}, test.extraArgs...)
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
	for _, sessionDirectory := range []bool{false, true} {
		name := "raw"
		if sessionDirectory {
			name = "session directory"
		}
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture")
			if err := os.WriteFile(output, []byte("existing"), 0o644); err != nil {
				t.Fatal(err)
			}
			var hardwareCalls int
			dependencies := preflightOnlyDebugCaptureDependencies(&hardwareCalls)
			arguments := debugCaptureArguments(config, output)
			if sessionDirectory {
				arguments = append(arguments, "--session-dir")
			}

			err := runDebugCaptureCaptureWithDependencies(
				arguments,
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
		})
	}
}

func TestDebugCaptureCaptureUsesDCAThenControllerAndSession(t *testing.T) {
	config := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	outputPath := filepath.Join(t.TempDir(), "capture.bin")
	var events []string
	dcaClient := &fakeDebugCaptureDCA{events: &events, closeErr: errors.New("late DCA close failure")}
	controller := &fakeDebugCaptureController{events: &events, closeErr: errors.New("late controller close failure")}
	dependencies := preflightOnlyDebugCaptureDependencies(nil)
	dependencies.checkAssets = func(string, string) (debugcapture.Assets, error) {
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
		if err := output.CommitContext(ctx); err != nil {
			return dca.CaptureStats{}, err
		}
		return dca.CaptureStats{OutputBytes: plan.ExpectedBytes}, nil
	}

	if err := runDebugCaptureCaptureWithDependencies(
		append(debugCaptureArguments(config, outputPath), "--sop2-reset"),
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
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("published output: %v", err)
	}
	assertPathDoesNotExist(t, outputPath+".part")
}

func TestDebugCaptureSessionDirectoryPublishesV1FromConfigSnapshot(t *testing.T) {
	configContents := strings.Replace(
		debugCaptureTestConfig,
		"frameCfg 0 1 32 100 100 1 0",
		"frameCfg 0 1 1 1 10 1 0",
		1,
	)
	config := writeDebugCaptureConfig(t, configContents)
	outputPath := filepath.Join(t.TempDir(), "capture-session")
	var events []string
	dcaClient := &fakeDebugCaptureDCA{events: &events}
	controller := &fakeDebugCaptureController{events: &events}
	dependencies := preflightOnlyDebugCaptureDependencies(nil)
	dependencies.checkAssets = func(string, string) (debugcapture.Assets, error) {
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
		_ session.Options,
	) (dca.CaptureStats, error) {
		events = append(events, "session")
		adcBytes = make([]byte, int(plan.ExpectedBytes))
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

	arguments := append(debugCaptureArguments(config, outputPath), "--session-dir")
	if err := runDebugCaptureCaptureWithDependencies(
		arguments,
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
	if string(archivedConfig) != configContents {
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
	if code := Run([]string{"debug-capture", "capture", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d: %s", code, stderr.String())
	}
	help := stdout.String() + stderr.String()
	for _, expected := range []string{"--enhanced-port", "--bss-fw", "--mss-fw", "--d2xx-serial", "--d2xx-description", "--sop2-reset", "session-dir"} {
		if !strings.Contains(help, expected) {
			t.Errorf("help does not contain %s: %s", expected, help)
		}
	}
	for _, forbidden := range []string{"--no-reconfig", "--baud", "--serial-timeout-ms"} {
		if strings.Contains(help, forbidden) {
			t.Errorf("help contains forbidden text-CLI flag %s: %s", forbidden, help)
		}
	}
	if !captureInvocationHasCleanup([]string{"debug-capture", "capture"}) {
		t.Fatal("debug-capture capture was not classified for cancellation cleanup")
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
	return []string{
		config,
		output,
		"--enhanced-port", "COM3",
		"--bss-fw", "bss.bin",
		"--mss-fw", "mss.bin",
		"--d2xx-serial", "FT1234",
	}
}

func preflightOnlyDebugCaptureDependencies(hardwareCalls *int) debugCaptureDependencies {
	return debugCaptureDependencies{
		checkAssets: func(string, string) (debugcapture.Assets, error) {
			return debugcapture.Assets{}, nil
		},
		checkNative: func() error { return nil },
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

package app

import (
	"bytes"
	"context"
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

	tests := []struct {
		name      string
		config    string
		extraArgs []string
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
	output := filepath.Join(t.TempDir(), "capture.bin")
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
	assertPathDoesNotExist(t, output+".part")
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

func TestDebugCaptureCaptureHelpHasNoTextCLIRouteFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"debug-capture", "capture", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d: %s", code, stderr.String())
	}
	help := stdout.String() + stderr.String()
	for _, expected := range []string{"--enhanced-port", "--bss-fw", "--mss-fw", "--d2xx-serial", "--d2xx-description", "--sop2-reset"} {
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

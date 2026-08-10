package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturemanifest"
	"mmwcli/internal/capturestream"
	"mmwcli/internal/d2xx"
	"mmwcli/internal/dca"
	"mmwcli/internal/debugcapture"
	"mmwcli/internal/radar"
	"mmwcli/internal/session"
)

type debugCaptureDCA interface {
	session.DCAControl
	Close() error
}

type debugCaptureController interface {
	session.Radar
	Close() error
}

type debugCaptureSessionRunner func(
	context.Context,
	session.Radar,
	session.DCAControl,
	session.ReceiverFactory,
	radar.CapturePlan,
	capturefile.Output,
	session.Options,
) (dca.CaptureStats, error)

type debugCaptureDependencies struct {
	stdin          io.Reader
	checkAssets    func(radar.DeviceFamily, string, string) (debugcapture.Assets, error)
	validateFPGA   func(radar.DeviceFamily, dca.FPGAConfig) error
	buildLinkPlan  func(radar.DeviceFamily, radar.CapturePlan) (debugcapture.Plan, error)
	checkNative    func() error
	dialDCA        func(dca.Options) (debugCaptureDCA, error)
	openController func(context.Context, debugcapture.ControllerOptions) (debugCaptureController, error)
	newReceiver    session.ReceiverFactory
	runSession     debugCaptureSessionRunner
	context        func() (context.Context, context.CancelFunc)
}

func productionDebugCaptureDependencies() debugCaptureDependencies {
	return debugCaptureDependencies{
		stdin:         os.Stdin,
		checkAssets:   debugcapture.CheckAssetsForFamily,
		validateFPGA:  debugcapture.ValidateRawCaptureFPGAConfigForFamily,
		buildLinkPlan: debugcapture.BuildPlanForFamily,
		checkNative: func() error {
			library, err := loadNativeD2XX()
			if err != nil {
				return err
			}
			return library.Close()
		},
		dialDCA: func(options dca.Options) (debugCaptureDCA, error) {
			return dca.Dial(options)
		},
		openController: func(
			ctx context.Context,
			options debugcapture.ControllerOptions,
		) (debugCaptureController, error) {
			return debugcapture.OpenController(ctx, options)
		},
		newReceiver: func(config dca.ReceiverConfig) (session.DataReceiver, error) {
			return dca.NewReceiver(config)
		},
		runSession: session.Run,
		context:    hardwareSignalContext,
	}
}

func runDebugCaptureCapture(arguments []string, stdout, stderr io.Writer) error {
	return runDebugCaptureCaptureWithDependencies(
		arguments,
		stdout,
		stderr,
		productionDebugCaptureDependencies(),
	)
}

func runDebugCaptureCaptureWithDependencies(
	arguments []string,
	stdout, stderr io.Writer,
	dependencies debugCaptureDependencies,
) error {
	flags := newCommandFlagSet(
		"debug-cli capture",
		stderr,
		"mmwcli debug-cli capture CFG OUTDIR --family FAMILY --enhanced-port PORT --bss-fw FILE --mss-fw FILE (--d2xx-serial BASE | --d2xx-description BASE) [--sop2-reset] [options]",
	)
	familyName := flags.String("family", "", "exact radar family: xwr16xx, xwr18xx, or xwr68xx")
	enhancedPort := flags.String("enhanced-port", "", "Enhanced COM port used for SOP2 firmware submission")
	bssPath := flags.String("bss-fw", "", "selected-family BSS/RadarSS firmware file")
	mssPath := flags.String("mss-fw", "", "selected-family MSS/MasterSS firmware file")
	serialBase := flags.String("d2xx-serial", "", "D2XX serial-number base for the A/B interfaces")
	descriptionBase := flags.String("d2xx-description", "", "D2XX description base for the A/B interfaces")
	sop2Reset := flags.Bool("sop2-reset", false, "set SOP2 with D2XX C/D and pulse target reset before Enhanced COM")
	streamOutput := flags.Bool("stream", false, "also emit capture-stream v1 on stdout")
	multisensorPlanPath := flags.String("multisensor-plan", "", "external sensor plan JSON file")
	var frameCount optionalFrameCount
	flags.Var(&frameCount, "frame-count", "override frameCfg count (0 means until gracefully stopped)")
	stopOnStdinEOF := flags.Bool(
		"stop-on-stdin-eof",
		false,
		"gracefully stop an infinite capture when stdin closes",
	)
	dcaValues := addDCAFlags(flags, "dca-timeout-ms", dcaConfigurationFlags|dcaReceiverFlags)

	if len(arguments) != 0 && isHelp(arguments[0]) {
		return parseCommandFlags(flags, arguments)
	}
	if len(arguments) < 2 || strings.HasPrefix(arguments[0], "-") || strings.HasPrefix(arguments[1], "-") {
		return usageError{message: "debug-cli capture requires a CFG file and output directory"}
	}
	configPath, outputPath := arguments[0], arguments[1]
	if err := parseCommandFlags(flags, arguments[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected debug-cli capture arguments: " + strings.Join(flags.Args(), " ")}
	}
	device, err := parseDebugCaptureDeviceFamily(*familyName)
	if err != nil {
		return err
	}
	if err := validateDebugCaptureDependencies(dependencies); err != nil {
		return err
	}
	if err := requireExactValue("--enhanced-port", *enhancedPort); err != nil {
		return err
	}
	if err := requireExactValue("--bss-fw", *bssPath); err != nil {
		return err
	}
	if err := requireExactValue("--mss-fw", *mssPath); err != nil {
		return err
	}
	selectors, err := buildDebugCaptureSelectors(*serialBase, *descriptionBase)
	if err != nil {
		return err
	}
	streamStdout, err := requireCaptureStreamStdout(*streamOutput, stdout)
	if err != nil {
		return err
	}
	diagnostics := stdout
	if *streamOutput {
		diagnostics = stderr
	}

	dcaOptions, err := buildDCAOptions(dcaValues)
	if err != nil {
		return err
	}
	if err := dependencies.validateFPGA(device, dcaOptions.fpga); err != nil {
		return usageError{message: err.Error()}
	}

	prepared, err := loadDebugCaptureOutputPlanWithFrameCount(
		device,
		configPath,
		frameCount.pointer(),
	)
	if err != nil {
		return err
	}
	plan := prepared.plan
	if plan.InfiniteFrames {
		if !*stopOnStdinEOF {
			return usageError{message: "an infinite capture requires --stop-on-stdin-eof for graceful publication"}
		}
		if *streamOutput {
			return usageError{message: "--stream requires a finite frame count"}
		}
	} else if *stopOnStdinEOF {
		return usageError{message: "--stop-on-stdin-eof is valid only when the effective frame count is 0"}
	}
	linkPlan, err := dependencies.buildLinkPlan(device, plan)
	if err != nil {
		return err
	}
	if err := preflightDebugCaptureBounds(plan, &dcaOptions, diagnostics); err != nil {
		return err
	}
	assets, err := dependencies.checkAssets(device, *bssPath, *mssPath)
	if err != nil {
		return err
	}
	// This checks only that the native library can be loaded. It neither
	// enumerates nor opens D2XX devices, and prevents firmware submission from
	// preceding discovery of an unavailable optional backend.
	if err := dependencies.checkNative(); err != nil {
		return err
	}
	printCapturePlan(diagnostics, plan)

	stats, err := runDebugCaptureHardware(
		diagnostics,
		stderr,
		streamStdout,
		outputPath,
		prepared,
		*enhancedPort,
		assets,
		selectors,
		*sop2Reset,
		linkPlan,
		dcaOptions,
		*multisensorPlanPath,
		*stopOnStdinEOF,
		dependencies,
	)
	if err != nil {
		return err
	}
	fmt.Fprintln(diagnostics, formatCaptureStats(stats))
	return nil
}

func validateDebugCaptureDependencies(dependencies debugCaptureDependencies) error {
	if dependencies.checkAssets == nil || dependencies.validateFPGA == nil ||
		dependencies.buildLinkPlan == nil || dependencies.checkNative == nil ||
		dependencies.dialDCA == nil || dependencies.openController == nil ||
		dependencies.newReceiver == nil || dependencies.runSession == nil ||
		dependencies.context == nil || dependencies.stdin == nil {
		return errors.New("debug-cli dependencies are incomplete")
	}
	return nil
}

func parseDebugCaptureDeviceFamily(name string) (radar.DeviceFamily, error) {
	if name == "" {
		return radar.DeviceFamily{}, usageError{
			message: "--family is required; use exact xwr16xx, xwr18xx, or xwr68xx",
		}
	}
	device, err := debugcapture.ParseDeviceFamily(name)
	if err != nil {
		return radar.DeviceFamily{}, usageError{message: err.Error()}
	}
	return device, nil
}

func loadDebugCaptureOutputPlan(
	device radar.DeviceFamily,
	path string,
) (preparedCaptureOutput, error) {
	return loadDebugCaptureOutputPlanWithFrameCount(device, path, nil)
}

func loadDebugCaptureOutputPlanWithFrameCount(
	device radar.DeviceFamily,
	path string,
	frameCount *uint16,
) (preparedCaptureOutput, error) {
	snapshot, err := readCaptureSessionConfig(path)
	if err != nil {
		return preparedCaptureOutput{}, err
	}
	if frameCount != nil {
		snapshot, err = radar.OverrideCaptureSessionV1FrameCount(snapshot, *frameCount)
		if err != nil {
			return preparedCaptureOutput{}, err
		}
	}
	plan, err := radar.BuildCaptureSessionV1PlanForFamily(device, snapshot)
	if err != nil {
		return preparedCaptureOutput{}, err
	}
	finalize, err := capturemanifest.NewV1Finalizer(snapshot, plan.RawCapture)
	if err != nil {
		return preparedCaptureOutput{}, err
	}
	return preparedCaptureOutput{
		plan:            plan,
		configSnapshot:  snapshot,
		finalizeSession: finalize,
	}, nil
}

func requireExactValue(option, value string) error {
	if strings.TrimSpace(value) == "" {
		return usageError{message: option + " is required"}
	}
	if value != strings.TrimSpace(value) {
		return usageError{message: option + " must not contain leading or trailing whitespace"}
	}
	if strings.IndexByte(value, 0) >= 0 {
		return usageError{message: option + " contains NUL"}
	}
	return nil
}

func buildDebugCaptureSelectors(serialBase, descriptionBase string) (debugcapture.D2XXSelectors, error) {
	serialSet := serialBase != ""
	descriptionSet := descriptionBase != ""
	if serialSet == descriptionSet {
		return debugcapture.D2XXSelectors{}, usageError{
			message: "exactly one of --d2xx-serial BASE or --d2xx-description BASE is required",
		}
	}

	if serialSet {
		if err := requireExactValue("--d2xx-serial", serialBase); err != nil {
			return debugcapture.D2XXSelectors{}, err
		}
		selectors := debugcapture.D2XXSelectors{
			SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: serialBase + "A"},
			IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: serialBase + "B"},
		}
		if err := validateDerivedSelectors(selectors); err != nil {
			return debugcapture.D2XXSelectors{}, err
		}
		return selectors, nil
	}

	if err := requireExactValue("--d2xx-description", descriptionBase); err != nil {
		return debugcapture.D2XXSelectors{}, err
	}
	selectors := debugcapture.D2XXSelectors{
		SPI: d2xx.Selector{By: d2xx.SelectByDescription, Value: descriptionBase + " A"},
		IRQ: d2xx.Selector{By: d2xx.SelectByDescription, Value: descriptionBase + " B"},
	}
	if err := validateDerivedSelectors(selectors); err != nil {
		return debugcapture.D2XXSelectors{}, err
	}
	return selectors, nil
}

func validateDerivedSelectors(selectors debugcapture.D2XXSelectors) error {
	if err := selectors.SPI.Validate(); err != nil {
		return usageError{message: "invalid D2XX SPI selector: " + err.Error()}
	}
	if err := selectors.IRQ.Validate(); err != nil {
		return usageError{message: "invalid D2XX IRQ selector: " + err.Error()}
	}
	return nil
}

func preflightDebugCaptureBounds(
	plan radar.CapturePlan,
	options *dcaCommandOptions,
	stdout io.Writer,
) error {
	if plan.ExpectedDCADataFormat != options.fpga.DataFormat {
		return usageError{message: fmt.Sprintf(
			"radar adcCfg requires DCA data-format=%d, configured=%d",
			plan.ExpectedDCADataFormat,
			options.fpga.DataFormat,
		)}
	}
	if plan.ExpectedBytes > 0 {
		if options.receiver.MaxOutputBytes < plan.ExpectedBytes {
			return usageError{message: fmt.Sprintf(
				"--max-bytes=%d is smaller than the CFG-derived finite capture size %d",
				options.receiver.MaxOutputBytes,
				plan.ExpectedBytes,
			)}
		}
		options.receiver.MaxOutputBytes = plan.ExpectedBytes
	}
	requiredIdleTimeout, err := session.MinimumReceiverIdleTimeout(plan)
	if err != nil {
		return usageError{message: err.Error()}
	}
	if options.receiver.IdleTimeout < requiredIdleTimeout {
		fmt.Fprintf(
			stdout,
			"[capture] idle timeout raised from %s to %s for DCA raw tail/frame aggregation\n",
			options.receiver.IdleTimeout,
			requiredIdleTimeout,
		)
		options.receiver.IdleTimeout = requiredIdleTimeout
	}
	requiredFirstPacketTimeout, err := session.MinimumFirstPacketTimeout(plan)
	if err != nil {
		return usageError{message: err.Error()}
	}
	if options.receiver.FirstPacketTimeout < requiredFirstPacketTimeout {
		fmt.Fprintf(
			stdout,
			"[capture] first-packet timeout raised from %s to %s for DCA packet aggregation\n",
			options.receiver.FirstPacketTimeout,
			requiredFirstPacketTimeout,
		)
		options.receiver.FirstPacketTimeout = requiredFirstPacketTimeout
	}
	if _, finite, err := plan.MaximumStreamingDuration(options.receiver.IdleTimeout); err != nil {
		return usageError{message: err.Error()}
	} else if plan.ExpectedBytes > 0 && !finite {
		return usageError{message: "finite radar capture plan has no bounded streaming duration"}
	}
	return nil
}

func runDebugCaptureHardware(
	stdout io.Writer,
	producerStderr io.Writer,
	streamStdout *os.File,
	outputPath string,
	prepared preparedCaptureOutput,
	enhancedPort string,
	assets debugcapture.Assets,
	selectors debugcapture.D2XXSelectors,
	resetSOP2 bool,
	linkPlan debugcapture.Plan,
	dcaOptions dcaCommandOptions,
	multisensorPlanPath string,
	stopOnStdinEOF bool,
	dependencies debugCaptureDependencies,
) (stats dca.CaptureStats, resultErr error) {
	plan := prepared.plan
	// Reserving the OUT.part file or directory is the final preflight and
	// precedes every hardware I/O.
	ctx, stopSignal := dependencies.context()
	defer stopSignal()
	output, aggregate, err := createCaptureDestination(
		ctx, outputPath, multisensorPlanPath, prepared, producerStderr, streamStdout, stopSignal,
	)
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = joinDebugCaptureCleanup(resultErr, "capture output", output.Close())
	}()
	if aggregate != nil {
		defer func() { resultErr = aggregate.finish(ctx, resultErr) }()
	}
	var stream *activeCaptureStream
	if streamStdout != nil && aggregate == nil {
		stream, err = startCaptureStream(
			ctx,
			streamStdout,
			output,
			prepared,
			capturestream.CaptureModeDebugCLI,
			stopSignal,
		)
		if err != nil {
			return stats, err
		}
		defer func() { resultErr = stream.finish(resultErr) }()
	}
	dcaClient, err := dependencies.dialDCA(dcaOptions.control)
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = closeCaptureClient(
			resultErr,
			output.Committed(),
			"DCA1000 control client",
			dcaClient,
		)
	}()

	controller, err := dependencies.openController(ctx, debugcapture.ControllerOptions{
		EnhancedPort: enhancedPort,
		Assets:       assets,
		Selectors:    selectors,
		Plan:         linkPlan,
		ResetSOP2:    resetSOP2,
	})
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = closeCaptureClient(
			resultErr,
			output.Committed(),
			"debug-cli controller",
			controller,
		)
	}()

	sessionOptions := session.DefaultOptions()
	sessionOptions.FPGAConfig = dcaOptions.fpga
	sessionOptions.ReceiverConfig = dcaOptions.receiver
	sessionOptions.PacketDelay = dcaOptions.delay
	sessionOptions.ResetFPGA = dcaOptions.reset
	sessionOptions.CommandTimeout = dcaOptions.control.Timeout
	sessionOptions.DrainTimeout = 3 * time.Second
	if stream != nil {
		sessionOptions.Mirror = stream.mirror
	} else if aggregate != nil && aggregate.stream != nil {
		sessionOptions.Mirror = aggregate.stream.mirror
	}
	if aggregate != nil {
		sessionOptions.Participant = aggregate
	}
	if stopOnStdinEOF {
		sessionOptions.StopRequested = stopWhenReaderEnds(dependencies.stdin)
	}
	sessionOptions.Log = func(message string) { fmt.Fprintln(stdout, "[capture] "+message) }
	stats, err = dependencies.runSession(
		ctx,
		controller,
		dcaClient,
		dependencies.newReceiver,
		plan,
		output,
		sessionOptions,
	)
	if err != nil {
		return stats, err
	}
	if !output.Committed() {
		return stats, errors.New("debug-cli session completed without publishing the capture output")
	}
	return stats, nil
}

func joinDebugCaptureCleanup(resultErr error, resource string, closeErr error) error {
	if closeErr == nil {
		return resultErr
	}
	marked := &session.CleanupError{Err: fmt.Errorf("close %s: %w", resource, closeErr)}
	return errors.Join(resultErr, marked)
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"mmwcli/internal/capturefile"
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
	*capturefile.File,
	session.Options,
) (dca.CaptureStats, error)

type debugCaptureDependencies struct {
	checkAssets    func(string, string) (debugcapture.Assets, error)
	checkNative    func() error
	dialDCA        func(dca.Options) (debugCaptureDCA, error)
	openController func(context.Context, debugcapture.ControllerOptions) (debugCaptureController, error)
	newReceiver    session.ReceiverFactory
	runSession     debugCaptureSessionRunner
	context        func() (context.Context, context.CancelFunc)
}

func productionDebugCaptureDependencies() debugCaptureDependencies {
	return debugCaptureDependencies{
		checkAssets: debugcapture.CheckAssets,
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
		"debug-capture capture",
		stderr,
		"mmwcli debug-capture capture CFG OUT --enhanced-port PORT --bss-fw FILE --mss-fw FILE (--d2xx-serial BASE | --d2xx-description BASE) [options]",
	)
	enhancedPort := flags.String("enhanced-port", "", "Enhanced COM port used for SOP2 firmware submission")
	bssPath := flags.String("bss-fw", "", "xWR68xx BSS/RadarSS firmware file")
	mssPath := flags.String("mss-fw", "", "xWR68xx MSS/MasterSS firmware file")
	serialBase := flags.String("d2xx-serial", "", "D2XX serial-number base for the A/B interfaces")
	descriptionBase := flags.String("d2xx-description", "", "D2XX description base for the A/B interfaces")
	dcaValues := addDCAFlags(flags, "dca-timeout-ms", dcaConfigurationFlags|dcaReceiverFlags)

	if len(arguments) != 0 && isHelp(arguments[0]) {
		return parseCommandFlags(flags, arguments)
	}
	if len(arguments) < 2 || strings.HasPrefix(arguments[0], "-") || strings.HasPrefix(arguments[1], "-") {
		return usageError{message: "debug-capture capture requires CFG and output files"}
	}
	configPath, outputPath := arguments[0], arguments[1]
	if err := parseCommandFlags(flags, arguments[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected debug-capture capture arguments: " + strings.Join(flags.Args(), " ")}
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

	dcaOptions, err := buildDCAOptions(dcaValues)
	if err != nil {
		return err
	}
	if err := requireRawCaptureFPGAConfig(dcaOptions.fpga); err != nil {
		return err
	}

	plan, err := loadCapturePlan(radar.StudioCLI, configPath, radar.FullConfiguration)
	if err != nil {
		return err
	}
	linkPlan, err := debugcapture.BuildPlan(plan)
	if err != nil {
		return err
	}
	if err := preflightDebugCaptureBounds(plan, &dcaOptions, stdout); err != nil {
		return err
	}
	assets, err := dependencies.checkAssets(*bssPath, *mssPath)
	if err != nil {
		return err
	}
	// This checks only that the native library can be loaded. It neither
	// enumerates nor opens D2XX devices, and prevents firmware submission from
	// preceding discovery of an unavailable optional backend.
	if err := dependencies.checkNative(); err != nil {
		return err
	}
	printCapturePlan(stdout, plan)

	stats, err := runDebugCaptureHardware(
		stdout,
		outputPath,
		*enhancedPort,
		assets,
		selectors,
		linkPlan,
		plan,
		dcaOptions,
		dependencies,
	)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, formatCaptureStats(stats))
	return nil
}

func validateDebugCaptureDependencies(dependencies debugCaptureDependencies) error {
	if dependencies.checkAssets == nil || dependencies.checkNative == nil ||
		dependencies.dialDCA == nil || dependencies.openController == nil ||
		dependencies.newReceiver == nil || dependencies.runSession == nil ||
		dependencies.context == nil {
		return errors.New("debug-capture dependencies are incomplete")
	}
	return nil
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
	outputPath, enhancedPort string,
	assets debugcapture.Assets,
	selectors debugcapture.D2XXSelectors,
	linkPlan debugcapture.Plan,
	plan radar.CapturePlan,
	dcaOptions dcaCommandOptions,
	dependencies debugCaptureDependencies,
) (stats dca.CaptureStats, resultErr error) {
	// Reserving OUT.part is the final preflight and precedes every hardware I/O.
	output, err := capturefile.Create(outputPath)
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = joinDebugCaptureCleanup(resultErr, "capture output", output.Close())
	}()

	ctx, stopSignal := dependencies.context()
	defer stopSignal()
	dcaClient, err := dependencies.dialDCA(dcaOptions.control)
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = joinDebugCapturePostSessionClose(resultErr, "DCA1000 control client", dcaClient.Close())
	}()

	controller, err := dependencies.openController(ctx, debugcapture.ControllerOptions{
		EnhancedPort: enhancedPort,
		Assets:       assets,
		Selectors:    selectors,
		Plan:         linkPlan,
	})
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = joinDebugCapturePostSessionClose(resultErr, "debug-capture controller", controller.Close())
	}()

	sessionOptions := session.DefaultOptions()
	sessionOptions.FPGAConfig = dcaOptions.fpga
	sessionOptions.ReceiverConfig = dcaOptions.receiver
	sessionOptions.PacketDelay = dcaOptions.delay
	sessionOptions.ResetFPGA = dcaOptions.reset
	sessionOptions.CommandTimeout = dcaOptions.control.Timeout
	sessionOptions.DrainTimeout = 3 * time.Second
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
		return stats, errors.New("debug-capture session completed without publishing the capture output")
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

// Once session.Run returns success, OUT has already been atomically published
// after all hardware stop and receiver cleanup. A later local handle-close
// error cannot safely turn that committed capture back into a failed .part.
func joinDebugCapturePostSessionClose(resultErr error, resource string, closeErr error) error {
	if resultErr == nil || closeErr == nil {
		return resultErr
	}
	return joinDebugCaptureCleanup(resultErr, resource, closeErr)
}

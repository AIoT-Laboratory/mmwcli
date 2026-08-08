package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturemanifest"
	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
	"mmwcli/internal/serialport"
	"mmwcli/internal/session"
)

func runRadar(command string, arguments []string, stdout, stderr io.Writer) error {
	dialect := radar.StudioCLI
	if len(arguments) == 0 {
		return usageError{message: command + " requires check, version, apply, start, stop, or capture"}
	}
	if isHelp(arguments[0]) {
		printRadarHelp(stdout, command)
		return nil
	}
	action := strings.ToLower(arguments[0])
	switch action {
	case "check":
		return checkRadarConfig(dialect, arguments[1:], stdout, stderr)
	case "capture":
		return captureRadar(dialect, arguments[1:], stdout, stderr)
	case "version", "apply", "start", "stop":
		return controlRadar(dialect, action, arguments[1:], stdout, stderr)
	default:
		return usageError{message: "unknown " + command + " command: " + arguments[0]}
	}
}

func checkRadarConfig(dialect radar.Dialect, arguments []string, stdout, stderr io.Writer) error {
	flags := newCommandFlagSet(dialect.Name()+" check", stderr, "mmwcli "+dialect.Name()+" check CFG")
	if len(arguments) != 0 && isHelp(arguments[0]) {
		return parseCommandFlags(flags, arguments)
	}
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "-") {
		return usageError{message: dialect.Name() + " check requires a CFG file"}
	}
	if err := parseCommandFlags(flags, arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected check arguments: " + strings.Join(flags.Args(), " ")}
	}
	plan, err := loadCapturePlan(dialect, arguments[0], radar.FullConfiguration)
	if err != nil {
		return err
	}
	printCapturePlan(stdout, plan)
	return nil
}

func controlRadar(dialect radar.Dialect, action string, arguments []string, stdout, stderr io.Writer) error {
	var configPath string
	rest := arguments
	var plan radar.CapturePlan
	var err error

	usage := "mmwcli " + dialect.Name() + " " + action + " [options]"
	if action == "apply" {
		usage = "mmwcli " + dialect.Name() + " apply CFG [options]"
	}
	flags := newCommandFlagSet(dialect.Name()+" "+action, stderr, usage)
	portName := flags.String("port", "", "serial port (COM3 or /dev/ttyACM0)")
	baud := flags.Int("baud", dialect.DefaultBaud(), "serial baud")
	timeoutMS := flags.Int("serial-timeout-ms", 10000, "serial command timeout")
	noReconfigure := false
	if action == "start" {
		flags.BoolVar(&noReconfigure, "no-reconfig", false, "start using the existing radar configuration")
	}
	if action == "apply" {
		if len(rest) != 0 && isHelp(rest[0]) {
			return parseCommandFlags(flags, rest)
		}
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return usageError{message: dialect.Name() + " apply requires a CFG file"}
		}
		configPath = rest[0]
		rest = rest[1:]
	}
	if err := parseCommandFlags(flags, rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected " + action + " arguments: " + strings.Join(flags.Args(), " ")}
	}
	if *portName == "" {
		return usageError{message: "--port is required; mmwcli never scans serial ports"}
	}
	if *baud < 1200 || *baud > 4000000 {
		return usageError{message: "--baud must be in 1200..4000000"}
	}
	if *timeoutMS < 100 || *timeoutMS > 25500 {
		return usageError{message: "--serial-timeout-ms must be in 100..25500"}
	}
	if action == "apply" {
		// Complete preflight before opening the serial port.
		plan, err = loadCapturePlan(dialect, configPath, radar.FullConfiguration)
		if err != nil {
			return err
		}
	}

	transport, err := serialport.Open(*portName, *baud, time.Duration(*timeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	client, err := radar.NewClient(transport, dialect, time.Duration(*timeoutMS)*time.Millisecond)
	if err != nil {
		transport.Close()
		return err
	}
	defer client.Close()
	ctx, stopSignal := hardwareSignalContext()
	defer stopSignal()

	switch action {
	case "version":
		response, err := client.VerifyPlatformContext(ctx)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, response)
		return nil
	case "apply":
		if _, err := client.StopContext(ctx); err != nil {
			return err
		}
		if err := client.ApplyContext(ctx, plan); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "configuration applied without starting radar: %d commands\n", len(plan.ConfigurationCommands))
		return nil
	case "start":
		if noReconfigure {
			_, err = client.StartWithoutReconfigurationContext(ctx)
		} else {
			_, err = client.StartContext(ctx)
		}
		if err == nil {
			fmt.Fprintln(stdout, "radar started")
		}
		return err
	case "stop":
		_, err = client.StopContext(ctx)
		if err == nil {
			fmt.Fprintln(stdout, "radar stopped (sensor/frame only; board remains powered)")
		}
		return err
	default:
		panic("unreachable radar action")
	}
}

func captureRadar(dialect radar.Dialect, arguments []string, stdout, stderr io.Writer) (resultErr error) {
	flags := newCommandFlagSet(
		dialect.Name()+" capture",
		stderr,
		"mmwcli "+dialect.Name()+" capture CFG OUT [options]",
	)
	portName := flags.String("port", "", "serial port (COM3 or /dev/ttyACM0)")
	baud := flags.Int("baud", dialect.DefaultBaud(), "serial baud")
	serialTimeoutMS := flags.Int("serial-timeout-ms", 10000, "serial command timeout")
	noReconfigure := false
	sessionDirectory := false
	flags.BoolVar(&noReconfigure, "no-reconfig", false, "reuse the existing radar configuration")
	flags.BoolVar(&sessionDirectory, "session-dir", false, "publish ADC data and v1 metadata as an output directory")
	dcaValues := addDCAFlags(flags, "dca-timeout-ms", dcaConfigurationFlags|dcaReceiverFlags)
	if len(arguments) != 0 && isHelp(arguments[0]) {
		return parseCommandFlags(flags, arguments)
	}
	if len(arguments) < 2 || strings.HasPrefix(arguments[0], "-") || strings.HasPrefix(arguments[1], "-") {
		return usageError{message: dialect.Name() + " capture requires CFG and output paths"}
	}
	configPath, outputPath := arguments[0], arguments[1]
	if err := parseCommandFlags(flags, arguments[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected capture arguments: " + strings.Join(flags.Args(), " ")}
	}
	if *portName == "" {
		return usageError{message: "--port is required; mmwcli never scans serial ports"}
	}
	if *baud < 1200 || *baud > 4000000 {
		return usageError{message: "--baud must be in 1200..4000000"}
	}
	if *serialTimeoutMS < 100 || *serialTimeoutMS > 25500 {
		return usageError{message: "--serial-timeout-ms must be in 100..25500"}
	}
	mode := radar.FullConfiguration
	if noReconfigure {
		mode = radar.ReuseConfiguration
	}
	plan, finalizeSession, err := loadCaptureOutputPlan(dialect, configPath, mode, sessionDirectory)
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
	if plan.ExpectedDCADataFormat != dcaOptions.fpga.DataFormat {
		return usageError{message: fmt.Sprintf(
			"radar adcCfg requires DCA data-format=%d, configured=%d",
			plan.ExpectedDCADataFormat,
			dcaOptions.fpga.DataFormat,
		)}
	}
	if plan.ExpectedBytes > 0 {
		if dcaOptions.receiver.MaxOutputBytes < plan.ExpectedBytes {
			return usageError{message: fmt.Sprintf(
				"--max-bytes=%d is smaller than the CFG-derived finite capture size %d",
				dcaOptions.receiver.MaxOutputBytes,
				plan.ExpectedBytes,
			)}
		}
		// A finite plan has a stronger bound than the generic safety ceiling.
		dcaOptions.receiver.MaxOutputBytes = plan.ExpectedBytes
	}
	requiredIdleTimeout, err := session.MinimumReceiverIdleTimeout(plan)
	if err != nil {
		return usageError{message: err.Error()}
	}
	if dcaOptions.receiver.IdleTimeout < requiredIdleTimeout {
		fmt.Fprintf(
			stdout,
			"[capture] idle timeout raised from %s to %s for DCA raw tail/frame aggregation\n",
			dcaOptions.receiver.IdleTimeout,
			requiredIdleTimeout,
		)
		dcaOptions.receiver.IdleTimeout = requiredIdleTimeout
	}
	requiredFirstPacketTimeout, err := session.MinimumFirstPacketTimeout(plan)
	if err != nil {
		return usageError{message: err.Error()}
	}
	if dcaOptions.receiver.FirstPacketTimeout < requiredFirstPacketTimeout {
		fmt.Fprintf(
			stdout,
			"[capture] first-packet timeout raised from %s to %s for DCA packet aggregation\n",
			dcaOptions.receiver.FirstPacketTimeout,
			requiredFirstPacketTimeout,
		)
		dcaOptions.receiver.FirstPacketTimeout = requiredFirstPacketTimeout
	}
	if _, finite, err := plan.MaximumStreamingDuration(dcaOptions.receiver.IdleTimeout); err != nil {
		return usageError{message: err.Error()}
	} else if plan.ExpectedBytes > 0 && !finite {
		return usageError{message: "finite radar capture plan has no bounded streaming duration"}
	}
	printCapturePlan(stdout, plan)

	// Reserve the OUT.part file or directory before any hardware access.
	output, err := createCaptureOutput(outputPath, finalizeSession)
	if err != nil {
		return err
	}
	defer func() { resultErr = errorsJoin(resultErr, output.Close()) }()

	transport, err := serialport.Open(*portName, *baud, time.Duration(*serialTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	radarClient, err := radar.NewClient(transport, dialect, time.Duration(*serialTimeoutMS)*time.Millisecond)
	if err != nil {
		transport.Close()
		return err
	}
	defer func() {
		resultErr = closeCaptureClient(
			resultErr,
			output.Committed(),
			"radar client",
			radarClient,
		)
	}()
	dcaClient, err := dca.Dial(dcaOptions.control)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = closeCaptureClient(
			resultErr,
			output.Committed(),
			"DCA1000 control client",
			dcaClient,
		)
	}()

	ctx, stopSignal := hardwareSignalContext()
	defer stopSignal()
	sessionOptions := session.DefaultOptions()
	sessionOptions.FPGAConfig = dcaOptions.fpga
	sessionOptions.ReceiverConfig = dcaOptions.receiver
	sessionOptions.PacketDelay = dcaOptions.delay
	sessionOptions.ResetFPGA = dcaOptions.reset
	sessionOptions.CommandTimeout = dcaOptions.control.Timeout
	sessionOptions.DrainTimeout = 3 * time.Second
	sessionOptions.Log = func(message string) { fmt.Fprintln(stdout, "[capture] "+message) }
	stats, err := session.Run(
		ctx,
		radarClient,
		dcaClient,
		func(config dca.ReceiverConfig) (session.DataReceiver, error) {
			return dca.NewReceiver(config)
		},
		plan,
		output,
		sessionOptions,
	)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, formatCaptureStats(stats))
	return nil
}

func loadCapturePlan(dialect radar.Dialect, path string, mode radar.ConfigurationMode) (radar.CapturePlan, error) {
	commands, err := radar.ParseConfigFile(path)
	if err != nil {
		return radar.CapturePlan{}, err
	}
	return radar.BuildCapturePlan(dialect, commands, mode)
}

const captureSessionMaxConfigBytes = 4 << 20

func loadCaptureOutputPlan(
	dialect radar.Dialect,
	path string,
	mode radar.ConfigurationMode,
	sessionDirectory bool,
) (radar.CapturePlan, capturefile.SessionFinalizer, error) {
	if !sessionDirectory {
		plan, err := loadCapturePlan(dialect, path, mode)
		return plan, nil, err
	}
	if dialect != radar.StudioCLI {
		return radar.CapturePlan{}, nil, errors.New("capture session directories require the studio-cli dialect")
	}
	snapshot, err := readCaptureSessionConfig(path)
	if err != nil {
		return radar.CapturePlan{}, nil, err
	}
	plan, err := radar.BuildCaptureSessionV1Plan(snapshot, mode)
	if err != nil {
		return radar.CapturePlan{}, nil, err
	}
	finalize, err := capturemanifest.NewV1Finalizer(snapshot, capturemanifest.ADCLayoutGroup2IThenQ)
	if err != nil {
		return radar.CapturePlan{}, nil, err
	}
	return plan, finalize, nil
}

func readCaptureSessionConfig(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open capture session configuration %s: %w", path, err)
	}
	defer file.Close()
	snapshot, err := io.ReadAll(io.LimitReader(file, captureSessionMaxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read capture session configuration %s: %w", path, err)
	}
	if len(snapshot) > captureSessionMaxConfigBytes {
		return nil, fmt.Errorf(
			"capture session configuration %s exceeds %d bytes",
			path,
			captureSessionMaxConfigBytes,
		)
	}
	return snapshot, nil
}

func createCaptureOutput(path string, finalize capturefile.SessionFinalizer) (capturefile.Output, error) {
	if finalize == nil {
		return capturefile.Create(path)
	}
	return capturefile.CreateSessionDirectory(path, finalize)
}

func printCapturePlan(writer io.Writer, plan radar.CapturePlan) {
	frames := fmt.Sprintf("%d", plan.NumberOfFrames)
	if plan.InfiniteFrames {
		frames = "infinite"
	}
	fmt.Fprintf(writer, "CFG preflight: dialect=%s family=%s commands=%d start=%q data-format=%d frames=%s period=%s bytes-per-frame=%d expected-bytes=%d\n",
		plan.Dialect.Name(),
		plan.Dialect.DeviceFamily().Name(),
		len(plan.ConfigurationCommands),
		plan.StartCommand,
		plan.ExpectedDCADataFormat,
		frames,
		plan.FramePeriod,
		plan.BytesPerFrame,
		plan.ExpectedBytes,
	)
}

func errorsJoin(first, second error) error {
	return errors.Join(first, second)
}

package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturemanifest"
	"mmwcli/internal/capturestream"
	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
	"mmwcli/internal/serialport"
	"mmwcli/internal/session"
)

func runRadar(command string, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
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
		return captureRadar(dialect, arguments[1:], stdin, stdout, stderr)
	case "version", "apply", "start", "stop":
		return controlRadar(dialect, action, arguments[1:], stdout, stderr)
	default:
		return usageError{message: "unknown " + command + " command: " + arguments[0]}
	}
}

func checkRadarConfig(dialect radar.Dialect, arguments []string, stdout, stderr io.Writer) error {
	flags := newCommandFlagSet(dialect.Name()+" check", stderr, "mmwcli "+dialect.Name()+" check CFG")
	var frameCount optionalFrameCount
	flags.Var(&frameCount, "frame-count", "override frameCfg count (0 means until gracefully stopped)")
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
	plan, err := loadCapturePlanWithFrameCount(
		dialect,
		arguments[0],
		radar.FullConfiguration,
		frameCount.pointer(),
	)
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

func captureRadar(
	dialect radar.Dialect,
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (resultErr error) {
	flags := newCommandFlagSet(
		dialect.Name()+" capture",
		stderr,
		"mmwcli "+dialect.Name()+" capture CFG OUTDIR [options]",
	)
	portName := flags.String("port", "", "serial port (COM3 or /dev/ttyACM0)")
	baud := flags.Int("baud", dialect.DefaultBaud(), "serial baud")
	serialTimeoutMS := flags.Int("serial-timeout-ms", 10000, "serial command timeout")
	streamOutput := false
	flags.BoolVar(&streamOutput, "stream", false, "also emit the radar or aggregate stream on stdout")
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
		return usageError{message: dialect.Name() + " capture requires a CFG file and output directory"}
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
	streamStdout, err := requireCaptureStreamStdout(streamOutput, stdout)
	if err != nil {
		return err
	}
	prepared, err := loadCaptureOutputPlanWithFrameCount(dialect, configPath, frameCount.pointer())
	if err != nil {
		return err
	}
	plan := prepared.plan
	if err := validateCaptureDurationControls(
		plan,
		*stopOnStdinEOF,
		streamOutput,
		*multisensorPlanPath,
	); err != nil {
		return err
	}
	diagnostics := stdout
	if streamOutput {
		diagnostics = stderr
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
	if err := constrainCaptureOutputBytes(plan, &dcaOptions.receiver); err != nil {
		return err
	}
	requiredIdleTimeout, err := session.MinimumReceiverIdleTimeout(plan)
	if err != nil {
		return usageError{message: err.Error()}
	}
	if dcaOptions.receiver.IdleTimeout < requiredIdleTimeout {
		fmt.Fprintf(
			diagnostics,
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
			diagnostics,
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
	printCapturePlan(diagnostics, plan)

	var stats dca.CaptureStats
	ctx, stopSignal := hardwareSignalContext()
	defer stopSignal()
	// Reserve OUT.part and complete external READY before hardware access.
	output, aggregate, err := createCaptureDestination(
		ctx, outputPath, *multisensorPlanPath, prepared, dcaOptions.receiver.MaxOutputBytes,
		stderr, streamStdout, stopSignal,
	)
	if err != nil {
		return err
	}
	defer func() { resultErr = errorsJoin(resultErr, output.Close()) }()
	if aggregate != nil {
		defer func() {
			if resultErr == nil {
				fmt.Fprintln(diagnostics, formatCaptureStats(stats))
			}
		}()
		defer func() { resultErr = aggregate.finish(ctx, resultErr) }()
	}
	var stream *activeCaptureStream
	if streamOutput && aggregate == nil {
		stream, err = startCaptureStream(
			ctx,
			streamStdout,
			output,
			prepared,
			capturestream.CaptureModeStudioCLI,
			stopSignal,
		)
		if err != nil {
			return err
		}
		defer func() { resultErr = stream.finish(resultErr) }()
	}

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
	if *stopOnStdinEOF {
		sessionOptions.StopRequested = stopWhenReaderEnds(stdin)
	}
	sessionOptions.Log = func(message string) { fmt.Fprintln(diagnostics, "[capture] "+message) }
	stats, err = session.Run(
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
	if aggregate == nil {
		fmt.Fprintln(diagnostics, formatCaptureStats(stats))
	}
	return nil
}

func loadCapturePlan(dialect radar.Dialect, path string, mode radar.ConfigurationMode) (radar.CapturePlan, error) {
	return loadCapturePlanWithFrameCount(dialect, path, mode, nil)
}

func loadCapturePlanWithFrameCount(
	dialect radar.Dialect,
	path string,
	mode radar.ConfigurationMode,
	frameCount *uint16,
) (radar.CapturePlan, error) {
	if frameCount == nil {
		commands, err := radar.ParseConfigFile(path)
		if err != nil {
			return radar.CapturePlan{}, err
		}
		return radar.BuildCapturePlan(dialect, commands, mode)
	}
	snapshot, err := readCaptureSessionConfig(path)
	if err != nil {
		return radar.CapturePlan{}, err
	}
	effective, err := radar.OverrideCaptureSessionV1FrameCount(snapshot, *frameCount)
	if err != nil {
		return radar.CapturePlan{}, err
	}
	commands, err := radar.ParseConfig(bytes.NewReader(effective))
	if err != nil {
		return radar.CapturePlan{}, err
	}
	return radar.BuildCapturePlan(dialect, commands, mode)
}

type optionalFrameCount struct {
	set   bool
	value uint16
}

func (value *optionalFrameCount) Set(raw string) error {
	parsed, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return errors.New("frame count must be an integer in 0..65535")
	}
	value.set = true
	value.value = uint16(parsed)
	return nil
}

func (value *optionalFrameCount) String() string {
	if value == nil || !value.set {
		return ""
	}
	return strconv.FormatUint(uint64(value.value), 10)
}

func (value *optionalFrameCount) pointer() *uint16 {
	if value == nil || !value.set {
		return nil
	}
	result := value.value
	return &result
}

func stopWhenReaderEnds(reader io.Reader) <-chan struct{} {
	stopped := make(chan struct{})
	go func() {
		if reader != nil {
			_, _ = io.Copy(io.Discard, reader)
		}
		close(stopped)
	}()
	return stopped
}

func validateCaptureDurationControls(
	plan radar.CapturePlan,
	stopOnStdinEOF bool,
	streamOutput bool,
	multisensorPlanPath string,
) error {
	if plan.InfiniteFrames {
		if !stopOnStdinEOF {
			return usageError{message: "an infinite capture requires --stop-on-stdin-eof for graceful publication"}
		}
		if streamOutput && multisensorPlanPath == "" {
			return usageError{
				message: "an infinite --stream capture requires --multisensor-plan; capture-stream v1 is finite",
			}
		}
	} else if stopOnStdinEOF {
		return usageError{message: "--stop-on-stdin-eof is valid only when the effective frame count is 0"}
	}
	return nil
}

func constrainCaptureOutputBytes(plan radar.CapturePlan, receiver *dca.ReceiverConfig) error {
	if receiver == nil {
		return errors.New("DCA receiver configuration is nil")
	}
	if plan.InfiniteFrames {
		if receiver.MaxOutputBytes < plan.BytesPerFrame {
			return usageError{message: fmt.Sprintf(
				"--max-bytes=%d is smaller than one CFG-derived radar frame of %d bytes",
				receiver.MaxOutputBytes,
				plan.BytesPerFrame,
			)}
		}
		return nil
	}
	if receiver.MaxOutputBytes < plan.ExpectedBytes {
		return usageError{message: fmt.Sprintf(
			"--max-bytes=%d is smaller than the CFG-derived finite capture size %d",
			receiver.MaxOutputBytes,
			plan.ExpectedBytes,
		)}
	}
	// A finite plan has a stronger bound than the generic safety ceiling.
	receiver.MaxOutputBytes = plan.ExpectedBytes
	return nil
}

const captureSessionMaxConfigBytes = 4 << 20

type preparedCaptureOutput struct {
	plan            radar.CapturePlan
	configSnapshot  []byte
	finalizeSession capturefile.SessionFinalizer
}

func loadCaptureOutputPlan(dialect radar.Dialect, path string) (preparedCaptureOutput, error) {
	return loadCaptureOutputPlanWithFrameCount(dialect, path, nil)
}

func loadCaptureOutputPlanWithFrameCount(
	dialect radar.Dialect,
	path string,
	frameCount *uint16,
) (preparedCaptureOutput, error) {
	if dialect != radar.StudioCLI {
		return preparedCaptureOutput{}, errors.New("capture session directories require the studio-cli dialect")
	}
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
	plan, err := radar.BuildCaptureSessionV1Plan(snapshot, radar.FullConfiguration)
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

func createCaptureOutput(
	path string,
	finalize capturefile.SessionFinalizer,
) (*capturefile.SessionDirectory, error) {
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

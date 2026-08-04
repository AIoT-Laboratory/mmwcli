package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/dca"
	"mmwcli/internal/session"
)

type dcaCommandOptions struct {
	control  dca.Options
	receiver dca.ReceiverConfig
	fpga     dca.FPGAConfig
	delay    int
	reset    bool
}

type dcaFlagValues struct {
	host          string
	device        string
	configPort    int
	dataPort      int
	timeoutMS     int
	firstPacketMS int
	idleMS        int
	maxBytes      int64
	fpga          dca.FPGAConfig
	delay         int
	reset         bool
}

type dcaFlagScope uint8

const (
	dcaConfigurationFlags dcaFlagScope = 1 << iota
	dcaReceiverFlags
)

func runDCA(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return usageError{message: "dca requires ping, version, configure, start, stop, reset-fpga, reset-radar, or capture"}
	}
	if isHelp(arguments[0]) {
		printDCAHelp(stdout)
		return nil
	}
	action := strings.ToLower(arguments[0])
	known := map[string]bool{
		"ping": true, "version": true, "configure": true, "start": true, "stop": true,
		"reset-fpga": true, "reset-radar": true, "capture": true,
	}
	if !known[action] {
		return usageError{message: "unknown dca command: " + arguments[0]}
	}

	scope := dcaFlagScope(0)
	if action == "configure" {
		scope = dcaConfigurationFlags
	} else if action == "capture" {
		scope = dcaConfigurationFlags | dcaReceiverFlags
	}

	var outputPath string
	rest := arguments[1:]
	if action == "capture" {
		if len(rest) != 0 && isHelp(rest[0]) {
			_, err := parseDCAOptions("dca "+action, rest, stderr, scope)
			return err
		}
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return usageError{message: "dca capture requires an output file"}
		}
		outputPath = rest[0]
		rest = rest[1:]
	}

	options, err := parseDCAOptions("dca "+action, rest, stderr, scope)
	if err != nil {
		return err
	}
	if action == "capture" {
		if err := requireRawCaptureFPGAConfig(options.fpga); err != nil {
			return err
		}
		if options.receiver.IdleTimeout < dca.RawModeTailFlushGuard {
			fmt.Fprintf(
				stdout,
				"[capture] idle timeout raised from %s to %s for the DCA1000 raw tail flush\n",
				options.receiver.IdleTimeout,
				dca.RawModeTailFlushGuard,
			)
			options.receiver.IdleTimeout = dca.RawModeTailFlushGuard
		}
	}
	var captureOutput *capturefile.File
	if action == "capture" {
		captureOutput, err = capturefile.Create(outputPath)
		if err != nil {
			return err
		}
	}
	client, err := dca.Dial(options.control)
	if err != nil {
		if captureOutput != nil {
			return errors.Join(err, captureOutput.Close())
		}
		return err
	}
	defer client.Close()

	ctx, stopSignal := hardwareSignalContext()
	defer stopSignal()
	switch action {
	case "ping":
		response, err := client.Ping(ctx)
		if err != nil {
			return err
		}
		if err := requireDCAStatus(response); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "DCA1000 alive: OK, source="+client.LastResponseEndpoint().String())
		return nil
	case "version":
		version, err := client.Version(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "DCA1000 FPGA %s, source=%s\n", version.String(), client.LastResponseEndpoint())
		return nil
	case "reset-fpga", "reset-radar":
		command := dca.CommandResetFPGA
		if action == "reset-radar" {
			command = dca.CommandResetRadar
		}
		response, err := client.Execute(ctx, command, nil)
		if err != nil {
			return err
		}
		if err := requireDCAStatus(response); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "DCA1000 "+action+": OK")
		return nil
	case "start":
		response, err := client.StartRecordConvergent(ctx)
		if err != nil {
			return err
		}
		if err := requireDCAStatus(response); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "DCA1000 record start: OK")
		return nil
	case "stop":
		response, err := client.StopRecord(ctx)
		if err != nil {
			return err
		}
		if err := requireDCAStatus(response); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "DCA1000 record stop: OK")
		return nil
	case "configure":
		if err := configureDCA(ctx, client, options); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "DCA1000 configure: OK")
		return nil
	case "capture":
		return captureDCA(ctx, captureOutput, client, options, stdout)
	default:
		panic("unreachable DCA action")
	}
}

func parseDCAOptions(name string, arguments []string, stderr io.Writer, scope dcaFlagScope) (dcaCommandOptions, error) {
	synopsis := "mmwcli " + name + " [options]"
	if name == "dca capture" {
		synopsis = "mmwcli dca capture OUT [options]"
	}
	flags := newCommandFlagSet(name, stderr, synopsis)
	values := addDCAFlags(flags, "timeout-ms", scope)
	if err := parseCommandFlags(flags, arguments); err != nil {
		return dcaCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return dcaCommandOptions{}, usageError{message: "unexpected DCA arguments: " + strings.Join(flags.Args(), " ")}
	}
	return buildDCAOptions(values)
}

func addDCAFlags(flags *flag.FlagSet, timeoutName string, scope dcaFlagScope) *dcaFlagValues {
	control := dca.DefaultOptions()
	receiver := dca.DefaultReceiverConfig()
	values := &dcaFlagValues{
		host:          receiver.DataBindAddress.String(),
		device:        control.DeviceAddress.String(),
		configPort:    control.ControlPort,
		dataPort:      receiver.DataBindPort,
		timeoutMS:     int(control.Timeout / time.Millisecond),
		firstPacketMS: int(receiver.FirstPacketTimeout / time.Millisecond),
		idleMS:        int(receiver.IdleTimeout / time.Millisecond),
		maxBytes:      receiver.MaxOutputBytes,
		fpga:          dca.DefaultFPGAConfig(),
		delay:         25,
	}
	flags.StringVar(&values.device, "device", values.device, "DCA1000 IPv4 address")
	flags.IntVar(&values.configPort, "config-port", values.configPort, "DCA1000 control UDP port")
	flags.IntVar(&values.timeoutMS, timeoutName, values.timeoutMS, "DCA command timeout")
	if scope&dcaConfigurationFlags != 0 {
		flags.IntVar(&values.fpga.LogMode, "log-mode", values.fpga.LogMode, "DCA log mode")
		flags.IntVar(&values.fpga.LVDSMode, "lvds-mode", values.fpga.LVDSMode, "DCA LVDS lane mode")
		flags.IntVar(&values.fpga.TransferMode, "transfer-mode", values.fpga.TransferMode, "DCA transfer mode")
		flags.IntVar(&values.fpga.CaptureMode, "capture-mode", values.fpga.CaptureMode, "DCA capture mode")
		flags.IntVar(&values.fpga.DataFormat, "data-format", values.fpga.DataFormat, "DCA ADC data format")
		flags.IntVar(&values.fpga.Timer, "timer", values.fpga.Timer, "DCA FPGA timer")
		flags.IntVar(&values.delay, "delay-us", values.delay, "DCA packet delay in microseconds")
		flags.BoolVar(&values.reset, "reset", false, "reset the DCA FPGA before configuration")
	}
	if scope&dcaReceiverFlags != 0 {
		flags.StringVar(&values.host, "host", values.host, "host IPv4 address for DCA data")
		flags.IntVar(&values.dataPort, "data-port", values.dataPort, "DCA1000 data UDP port")
		flags.IntVar(&values.firstPacketMS, "start-timeout-ms", values.firstPacketMS, "first ADC packet timeout")
		flags.IntVar(&values.idleMS, "idle-ms", values.idleMS, "ADC quiet window")
		flags.Int64Var(&values.maxBytes, "max-bytes", values.maxBytes, "maximum raw output bytes")
	}
	return values
}

func buildDCAOptions(values *dcaFlagValues) (dcaCommandOptions, error) {
	control := dca.DefaultOptions()
	receiver := dca.DefaultReceiverConfig()
	deviceIP, err := parseIPv4("--device", values.device)
	if err != nil {
		return dcaCommandOptions{}, err
	}
	hostIP, err := parseIPv4("--host", values.host)
	if err != nil {
		return dcaCommandOptions{}, err
	}
	if values.configPort < 1 || values.configPort > 65535 || values.dataPort < 1 || values.dataPort > 65535 {
		return dcaCommandOptions{}, usageError{message: "--config-port and --data-port must be in 1..65535"}
	}
	if values.timeoutMS < 100 || values.timeoutMS > 60000 || values.firstPacketMS < 100 || values.firstPacketMS > 3600000 || values.idleMS < 100 || values.idleMS > 3600000 {
		return dcaCommandOptions{}, usageError{message: "invalid DCA timeout; command=100..60000 ms, start/idle=100..3600000 ms"}
	}
	if values.maxBytes <= 0 {
		return dcaCommandOptions{}, usageError{message: "--max-bytes must be positive"}
	}
	if _, err := dca.BuildFPGAConfig(values.fpga); err != nil {
		return dcaCommandOptions{}, usageError{message: err.Error()}
	}
	if _, err := dca.BuildRecordConfig(values.delay); err != nil {
		return dcaCommandOptions{}, usageError{message: err.Error()}
	}
	control.DeviceAddress = deviceIP
	control.ControlPort = values.configPort
	control.ControlBindPort = values.configPort
	control.Timeout = time.Duration(values.timeoutMS) * time.Millisecond
	receiver.DataBindAddress = hostIP
	receiver.DeviceIP = deviceIP
	receiver.DataBindPort = values.dataPort
	receiver.FirstPacketTimeout = time.Duration(values.firstPacketMS) * time.Millisecond
	receiver.IdleTimeout = time.Duration(values.idleMS) * time.Millisecond
	receiver.MaxOutputBytes = values.maxBytes
	return dcaCommandOptions{
		control:  control,
		receiver: receiver,
		fpga:     values.fpga,
		delay:    values.delay,
		reset:    values.reset,
	}, nil
}

func configureDCA(ctx context.Context, client *dca.Client, options dcaCommandOptions) error {
	var response dca.Response
	var err error
	if options.reset {
		response, err = client.Execute(ctx, dca.CommandResetFPGA, nil)
		if err != nil {
			return err
		}
		if err := requireDCAStatus(response); err != nil {
			return err
		}
	}
	_, err = client.Configure(ctx, options.fpga, options.delay)
	if err != nil {
		return err
	}
	return validateDCAAsyncStatuses(client.TakeAsyncStatuses())
}

func requireRawCaptureFPGAConfig(config dca.FPGAConfig) error {
	if err := dca.ValidateRawCaptureFPGAConfig(config); err != nil {
		return usageError{message: err.Error()}
	}
	return nil
}

func captureDCA(ctx context.Context, output *capturefile.File, client *dca.Client, options dcaCommandOptions, stdout io.Writer) (resultErr error) {
	defer func() {
		if closeErr := output.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, &session.CleanupError{Err: closeErr})
		}
	}()
	// Establish a known non-recording state before reconfiguration. This is a
	// single explicit StopRecord, not a SystemAlive/Ping readiness gate.
	stopResponse, err := client.StopRecord(ctx)
	if err != nil {
		return fmt.Errorf("establish stopped DCA state: %w", err)
	}
	if err := requireDCAStatus(stopResponse); err != nil {
		return err
	}
	if err := configureDCA(ctx, client, options); err != nil {
		return err
	}
	receiver, err := dca.NewReceiver(options.receiver)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := receiver.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, &session.CleanupError{Err: closeErr})
		}
	}()
	if err := receiver.Start(ctx, output); err != nil {
		return err
	}
	recording := false
	defer func() {
		if !recording {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), options.control.Timeout)
		response, cleanupErr := client.StopRecord(cleanupCtx)
		cancel()
		if cleanupErr == nil {
			cleanupErr = requireDCAStatus(response)
		}
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, &session.CleanupError{Err: cleanupErr})
		}
	}()
	if _, err := client.StartRecordConvergent(ctx); err != nil {
		return err
	}
	recording = true
	if err := validateDCAAsyncStatuses(client.TakeAsyncStatuses()); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "DCA1000 armed; waiting for radar data (Ctrl+C stops and keeps .part on cancellation)")
	stats, receiveErr := receiver.Wait(ctx)

	recording = false // The following StopRecord is the one allowed attempt.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), options.control.Timeout)
	stopResponse, stopErr := client.StopRecord(cleanupCtx)
	cancel()
	if stopErr == nil {
		stopErr = requireDCAStatus(stopResponse)
	}
	var controlDrainErr error
	if stopErr == nil {
		drainContext, drainCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		statuses, err := client.DrainAsyncStatuses(drainContext, 50*time.Millisecond)
		drainCancel()
		controlDrainErr = errors.Join(err, validateDCAAsyncStatuses(statuses))
	}
	cleanupErr := errors.Join(stopErr, controlDrainErr)
	if cancellationErr := ctx.Err(); cancellationErr != nil {
		receiveErr = errors.Join(receiveErr, cancellationErr)
	}
	if receiveErr != nil || cleanupErr != nil {
		if cleanupErr != nil {
			return errors.Join(receiveErr, &session.CleanupError{Err: cleanupErr})
		}
		return receiveErr
	}
	if err := validateCaptureStats(stats); err != nil {
		return err
	}
	if err := output.CommitContext(ctx); err != nil {
		return err
	}
	fmt.Fprintln(stdout, formatCaptureStats(stats))
	return nil
}

func requireDCAStatus(response dca.Response) error {
	if response.Status != 0 {
		return &dca.StatusError{Command: response.Command, Status: response.Status}
	}
	return nil
}

func parseIPv4(option, value string) (net.IP, error) {
	address := net.ParseIP(value)
	if address == nil || address.To4() == nil {
		return nil, usageError{message: option + " must be an IPv4 address: " + value}
	}
	return address.To4(), nil
}

func validateCaptureStats(stats dca.CaptureStats) error {
	if stats.PacketsReceived == 0 {
		return errors.New("DCA1000 capture received no data packets")
	}
	if stats.MissingBytes != 0 || stats.DiscardedBeforeBasePackets != 0 {
		return fmt.Errorf("DCA1000 capture is incomplete: missingBytes=%d discardedBeforeBase=%d", stats.MissingBytes, stats.DiscardedBeforeBasePackets)
	}
	if stats.MalformedPackets != 0 {
		return fmt.Errorf("DCA1000 capture received %d malformed packet(s) from the configured device", stats.MalformedPackets)
	}
	if stats.OverlappingPackets != 0 {
		return fmt.Errorf("DCA1000 capture received %d overlapping packet(s)", stats.OverlappingPackets)
	}
	return nil
}

func validateDCAAsyncStatuses(statuses []dca.Response) error {
	for _, status := range statuses {
		if dca.IsFatalSystemStatus(status.Status) {
			return fmt.Errorf("DCA1000 fatal async status: 0x%04X", status.Status)
		}
	}
	return nil
}

func formatCaptureStats(stats dca.CaptureStats) string {
	return fmt.Sprintf(
		"capture complete: packets=%d payload=%d output=%d gaps=%d outOfOrder=%d missing=%d",
		stats.PacketsReceived,
		stats.PayloadBytesReceived,
		stats.OutputBytes,
		stats.SequenceGaps,
		stats.OutOfOrderPackets,
		stats.MissingBytes,
	)
}

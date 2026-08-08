package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"mmwcli/internal/dca"
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
		return usageError{message: "dca requires ping, version, configure, start, stop, reset-fpga, or reset-radar"}
	}
	if isHelp(arguments[0]) {
		printDCAHelp(stdout)
		return nil
	}
	action := strings.ToLower(arguments[0])
	known := map[string]bool{
		"ping": true, "version": true, "configure": true, "start": true, "stop": true,
		"reset-fpga": true, "reset-radar": true,
	}
	if !known[action] {
		return usageError{message: "unknown dca command: " + arguments[0]}
	}

	scope := dcaFlagScope(0)
	if action == "configure" {
		scope = dcaConfigurationFlags
	}

	options, err := parseDCAOptions("dca "+action, arguments[1:], stderr, scope)
	if err != nil {
		return err
	}
	client, err := dca.Dial(options.control)
	if err != nil {
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
	default:
		panic("unreachable DCA action")
	}
}

func parseDCAOptions(name string, arguments []string, stderr io.Writer, scope dcaFlagScope) (dcaCommandOptions, error) {
	synopsis := "mmwcli " + name + " [options]"
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

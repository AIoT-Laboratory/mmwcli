package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"mmwcli/internal/toolbox"
)

const Version = "0.1.0-dev"

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

func Run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) == 0 || isHelp(arguments[0]) {
		printHelp(stdout)
		return 0
	}

	var err error
	switch strings.ToLower(arguments[0]) {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "mmwcli %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "toolbox":
		err = runToolbox(arguments[1:], stdout, stderr)
	case "doctor":
		err = runDoctor(arguments[1:], stdout, stderr)
	case "demo", "studio-cli":
		err = runRadar(arguments[0], arguments[1:], stdout, stderr)
	case "dca":
		err = runDCA(arguments[1:], stdout, stderr)
	default:
		err = usageError{message: "unknown command: " + arguments[0]}
	}
	if err == nil {
		return 0
	}
	if usage, ok := errors.AsType[usageError](err); ok {
		fmt.Fprintln(stderr, "argument error:", usage.Error())
		fmt.Fprintln(stderr, "run mmwcli help for usage")
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if captureInvocationHasCleanup(arguments) && cancellationOnly(err) {
		fmt.Fprintln(stderr, "capture cancelled; cleanup completed")
		return 130
	}
	fmt.Fprintln(stderr, "failed:", err)
	return 4
}

func captureInvocationHasCleanup(arguments []string) bool {
	if len(arguments) < 2 || !strings.EqualFold(arguments[1], "capture") {
		return false
	}
	switch strings.ToLower(arguments[0]) {
	case "demo", "studio-cli", "dca":
		return true
	default:
		return false
	}
}

type cleanupFailure interface {
	CleanupFailed() bool
}

// cancellationOnly rejects the tempting but unsafe shortcut of treating any
// joined tree that contains context.Canceled as a clean cancellation. Every
// leaf must be cancellation-related and no cleanup marker may report failure.
func cancellationOnly(err error) bool {
	if err == nil {
		return false
	}
	if failure, ok := err.(cleanupFailure); ok && failure.CleanupFailed() {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return cancellationOnly(wrapped.Unwrap())
	}
	return errors.Is(err, context.Canceled)
}

func hardwareSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runToolbox(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return usageError{message: "toolbox requires locate or verify"}
	}
	if isHelp(arguments[0]) {
		printToolboxHelp(stdout)
		return nil
	}
	action := strings.ToLower(arguments[0])
	if action != "locate" && action != "verify" {
		return usageError{message: "unknown toolbox command: " + arguments[0]}
	}
	flags := newCommandFlagSet("toolbox "+action, stderr, "mmwcli toolbox "+action+" [options]")
	root := flags.String("toolbox-root", "", "Radar Toolbox root or tools/studio_cli directory")
	if err := parseCommandFlags(flags, arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected toolbox arguments: " + strings.Join(flags.Args(), " ")}
	}
	installation, err := toolbox.Locate(*root)
	if err != nil {
		return err
	}
	toolbox.Print(stdout, installation)
	if action == "verify" {
		if err := toolbox.Verify(installation); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "strict verification passed: Radar Toolbox %s xWR68xx assets\n", installation.PackageVersion)
	}
	return nil
}

func isHelp(command string) bool {
	switch strings.ToLower(command) {
	case "help", "--help", "-h":
		return true
	default:
		return false
	}
}

func printHelp(writer io.Writer) {
	fmt.Fprintf(writer, "mmwcli %s - TI xWR68xx cross-platform CLI control\n\n", Version)
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  mmwcli version")
	fmt.Fprintln(writer, "  mmwcli doctor [--toolbox-root PATH]")
	fmt.Fprintln(writer, "  mmwcli toolbox locate|verify [--toolbox-root PATH]")
	fmt.Fprintln(writer, "  mmwcli demo check|apply|start|stop|capture ...")
	fmt.Fprintln(writer, "  mmwcli studio-cli check|version|apply|start|stop|capture ...")
	fmt.Fprintln(writer, "  mmwcli dca ping|version|configure|start|stop|reset-fpga|reset-radar|capture ...")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Use an explicit serial port such as COM3 or /dev/ttyACM0; mmwcli never probes ports.")
}

func printRadarHelp(writer io.Writer, command string) {
	command = strings.ToLower(command)
	fmt.Fprintf(writer, "usage: mmwcli %s check CFG\n", command)
	if command == "studio-cli" {
		fmt.Fprintf(writer, "       mmwcli %s version --port PORT [options]\n", command)
	}
	fmt.Fprintf(writer, "       mmwcli %s apply CFG --port PORT [options]\n", command)
	fmt.Fprintf(writer, "       mmwcli %s start|stop --port PORT [options]\n", command)
	fmt.Fprintf(writer, "       mmwcli %s capture CFG OUT --port PORT [options]\n", command)
}

func printDCAHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage: mmwcli dca ping|version|start|stop|reset-fpga|reset-radar [options]")
	fmt.Fprintln(writer, "       mmwcli dca configure [configuration options]")
	fmt.Fprintln(writer, "       mmwcli dca capture OUT [configuration and receiver options]")
}

func printToolboxHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage: mmwcli toolbox locate|verify [--toolbox-root PATH]")
}

func newCommandFlagSet(name string, output io.Writer, synopsis string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, "usage: "+synopsis)
		flags.PrintDefaults()
	}
	return flags
}

func parseCommandFlags(flags *flag.FlagSet, arguments []string) error {
	err := flags.Parse(arguments)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return usageError{message: err.Error()}
}

func runDoctor(arguments []string, stdout, stderr io.Writer) error {
	flags := newCommandFlagSet("doctor", stderr, "mmwcli doctor [--toolbox-root PATH]")
	root := flags.String("toolbox-root", "", "Radar Toolbox root or tools/studio_cli directory")
	if err := parseCommandFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected doctor arguments: " + strings.Join(flags.Args(), " ")}
	}
	installation, err := toolbox.Locate(*root)
	if err != nil {
		return err
	}
	toolbox.Print(stdout, installation)
	if err := toolbox.Verify(installation); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(stdout, "doctor passed (offline environment check only)")
	return nil
}

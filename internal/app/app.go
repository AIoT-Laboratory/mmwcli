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
)

// Version can be set with -ldflags.
var Version = "dev"

type usageError struct{ message string }

func (err usageError) Error() string { return err.message }

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
	case "check":
		err = runCheck(arguments[1:], stdout, stderr)
	case "capture":
		err = runCapture(arguments[1:], stdout, stderr)
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
	if strings.EqualFold(arguments[0], "capture") && cancellationOnly(err) {
		fmt.Fprintln(stderr, "capture cancelled; cleanup completed")
		return 130
	}
	fmt.Fprintln(stderr, "failed:", err)
	return 4
}

func runCheck(arguments []string, stdout, stderr io.Writer) error {
	configPath, flags, rigPath, frames, radarOnly, err := parseCaptureFlags(
		"check",
		"mmwcli check RADAR_CFG --rig RIG --frames N [--radar-only]",
		arguments,
		false,
		stderr,
	)
	if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected check arguments: " + strings.Join(flags.Args(), " ")}
	}
	rig, err := loadRig(*rigPath, *radarOnly)
	if err != nil {
		return err
	}
	return checkCapture(captureRequest{
		ConfigPath: configPath,
		Rig:        rig,
		Frames:     uint16(*frames),
		RadarOnly:  *radarOnly,
	}, stdout)
}

func runCapture(arguments []string, stdout, stderr io.Writer) error {
	configPath, flags, rigPath, frames, radarOnly, err := parseCaptureFlags(
		"capture",
		"mmwcli capture RADAR_CFG TAKE --rig RIG --frames N [--radar-only]",
		arguments,
		true,
		stderr,
	)
	if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected capture arguments: " + strings.Join(flags.Args(), " ")}
	}
	rig, err := loadRig(*rigPath, *radarOnly)
	if err != nil {
		return err
	}
	return capture(captureRequest{
		ConfigPath: configPath,
		OutputPath: arguments[1],
		Rig:        rig,
		Frames:     uint16(*frames),
		RadarOnly:  *radarOnly,
	}, stdout, stderr)
}

func parseCaptureFlags(
	name,
	synopsis string,
	arguments []string,
	wantOutput bool,
	stderr io.Writer,
) (string, *flag.FlagSet, *string, *int, *bool, error) {
	positionals := 1
	if wantOutput {
		positionals = 2
	}
	flags := newCommandFlagSet(name, stderr, synopsis)
	rigPath := flags.String("rig", "", "rig JSON")
	frames := flags.Int("frames", 0, "finite radar frame count")
	radarOnly := flags.Bool("radar-only", false, "capture radar without a camera")
	if len(arguments) != 0 && isHelp(arguments[0]) {
		return "", flags, rigPath, frames, radarOnly, parseCommandFlags(flags, arguments)
	}
	if len(arguments) < positionals {
		return "", flags, rigPath, frames, radarOnly, usageError{message: synopsis}
	}
	for _, value := range arguments[:positionals] {
		if value == "" || strings.HasPrefix(value, "-") {
			return "", flags, rigPath, frames, radarOnly, usageError{message: synopsis}
		}
	}
	if err := parseCommandFlags(flags, arguments[positionals:]); err != nil {
		return "", flags, rigPath, frames, radarOnly, err
	}
	if *frames < 1 || *frames > 65535 {
		return "", flags, rigPath, frames, radarOnly, usageError{
			message: "--frames must be in 1..65535",
		}
	}
	if strings.TrimSpace(*rigPath) == "" {
		return "", flags, rigPath, frames, radarOnly, usageError{message: "--rig is required"}
	}
	return arguments[0], flags, rigPath, frames, radarOnly, nil
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
	fmt.Fprintf(writer, "mmwcli %s - IWR6843 + DCA1000 capture\n\n", Version)
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  mmwcli check RADAR_CFG --rig RIG --frames N [--radar-only]")
	fmt.Fprintln(writer, "  mmwcli capture RADAR_CFG TAKE --rig RIG --frames N [--radar-only]")
	fmt.Fprintln(writer, "  mmwcli version")
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

func hardwareSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

type cleanupFailure interface {
	CleanupFailed() bool
}

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

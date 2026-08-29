package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"mmwcli/internal/camera"
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
	case "camera":
		err = runCamera(arguments[1:], stdout, stderr)
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
	options, err := parseCaptureOptions(
		"check",
		"mmwcli check RADAR_CFG --rig RIG --frames N [--camera DEVICE] [--radar-only]",
		arguments,
		false,
		stderr,
	)
	if err != nil {
		return err
	}
	rig, err := loadRig(options.rigPath, options.radarOnly, options.camera)
	if err != nil {
		return err
	}
	return checkCapture(captureRequest{
		ConfigPath: options.configPath,
		Rig:        rig,
		Frames:     options.frames,
		RadarOnly:  options.radarOnly,
	}, stdout)
}

func runCapture(arguments []string, stdout, stderr io.Writer) error {
	options, err := parseCaptureOptions(
		"capture",
		"mmwcli capture RADAR_CFG TAKE --rig RIG --frames N [--camera DEVICE] [--radar-only]",
		arguments,
		true,
		stderr,
	)
	if err != nil {
		return err
	}
	rig, err := loadRig(options.rigPath, options.radarOnly, options.camera)
	if err != nil {
		return err
	}
	return capture(captureRequest{
		ConfigPath: options.configPath,
		OutputPath: options.outputPath,
		Rig:        rig,
		Frames:     options.frames,
		RadarOnly:  options.radarOnly,
	}, stdout, stderr)
}

type captureOptions struct {
	configPath string
	outputPath string
	rigPath    string
	camera     string
	frames     uint16
	radarOnly  bool
}

func parseCaptureOptions(
	name,
	synopsis string,
	arguments []string,
	wantOutput bool,
	stderr io.Writer,
) (captureOptions, error) {
	positionals := 1
	if wantOutput {
		positionals = 2
	}
	flags := newCommandFlagSet(name, stderr, synopsis)
	rigPath := flags.String("rig", "", "rig JSON")
	frames := flags.Int("frames", 0, "finite radar frame count")
	cameraDevice := flags.String("camera", "", "camera ID from camera list")
	radarOnly := flags.Bool("radar-only", false, "capture radar without a camera")
	if len(arguments) != 0 && isHelp(arguments[0]) {
		return captureOptions{}, parseCommandFlags(flags, arguments)
	}
	if len(arguments) < positionals {
		return captureOptions{}, usageError{message: synopsis}
	}
	for _, value := range arguments[:positionals] {
		if value == "" || strings.HasPrefix(value, "-") {
			return captureOptions{}, usageError{message: synopsis}
		}
	}
	if err := parseCommandFlags(flags, arguments[positionals:]); err != nil {
		return captureOptions{}, err
	}
	if flags.NArg() != 0 {
		return captureOptions{}, usageError{
			message: "unexpected " + name + " arguments: " + strings.Join(flags.Args(), " "),
		}
	}
	if *frames < 1 || *frames > 65535 {
		return captureOptions{}, usageError{
			message: "--frames must be in 1..65535",
		}
	}
	if strings.TrimSpace(*rigPath) == "" {
		return captureOptions{}, usageError{message: "--rig is required"}
	}
	if *radarOnly && *cameraDevice != "" {
		return captureOptions{}, usageError{
			message: "--camera cannot be used with --radar-only",
		}
	}
	options := captureOptions{
		configPath: arguments[0], rigPath: *rigPath, camera: *cameraDevice,
		frames: uint16(*frames), radarOnly: *radarOnly,
	}
	if wantOutput {
		options.outputPath = arguments[1]
	}
	return options, nil
}

func runCamera(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 || isHelp(arguments[0]) {
		printCameraHelp(stdout)
		return nil
	}
	switch arguments[0] {
	case "list":
		rigPath, _, err := parseCameraFlags(
			"camera list", "mmwcli camera list --rig RIG", arguments[1:], false, stderr,
		)
		if err != nil {
			return err
		}
		rig, err := loadRig(rigPath, true, "")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		devices, err := camera.List(ctx)
		if err != nil {
			return err
		}
		var defaultID *string
		if rig.Camera != nil {
			configured := rig.Camera.Device
			for _, device := range devices {
				if configured == device.Name || configured == device.ID {
					configured = device.ID
					break
				}
			}
			defaultID = &configured
		}
		return json.NewEncoder(stdout).Encode(struct {
			Default *string         `json:"default"`
			Devices []camera.Device `json:"devices"`
		}{Default: defaultID, Devices: devices})
	case "preview":
		rigPath, cameraDevice, err := parseCameraFlags(
			"camera preview", "mmwcli camera preview --rig RIG [--camera ID]",
			arguments[1:], true, stderr,
		)
		if err != nil {
			return err
		}
		rig, err := loadRig(rigPath, false, cameraDevice)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		image, err := camera.Preview(ctx, *rig.Camera)
		if err != nil {
			return err
		}
		written, err := stdout.Write(image)
		if err == nil && written != len(image) {
			err = io.ErrShortWrite
		}
		return err
	default:
		return usageError{message: "unknown camera command: " + arguments[0]}
	}
}

func parseCameraFlags(name, synopsis string, arguments []string, allowCamera bool, stderr io.Writer) (string, string, error) {
	flags := newCommandFlagSet(name, stderr, synopsis)
	rigPath := flags.String("rig", "", "rig JSON")
	var cameraDevice string
	if allowCamera {
		flags.StringVar(&cameraDevice, "camera", "", "camera ID from camera list")
	}
	if err := parseCommandFlags(flags, arguments); err != nil {
		return "", "", err
	}
	if flags.NArg() != 0 {
		return "", "", usageError{message: "unexpected arguments: " + strings.Join(flags.Args(), " ")}
	}
	if strings.TrimSpace(*rigPath) == "" {
		return "", "", usageError{message: "--rig is required"}
	}
	return *rigPath, cameraDevice, nil
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
	fmt.Fprintln(writer, "  mmwcli check RADAR_CFG --rig RIG --frames N [--camera DEVICE] [--radar-only]")
	fmt.Fprintln(writer, "  mmwcli capture RADAR_CFG TAKE --rig RIG --frames N [--camera DEVICE] [--radar-only]")
	fmt.Fprintln(writer, "  mmwcli camera list --rig RIG")
	fmt.Fprintln(writer, "  mmwcli camera preview --rig RIG [--camera ID]")
	fmt.Fprintln(writer, "  mmwcli version")
}

func printCameraHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  mmwcli camera list --rig RIG")
	fmt.Fprintln(writer, "  mmwcli camera preview --rig RIG [--camera ID]")
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

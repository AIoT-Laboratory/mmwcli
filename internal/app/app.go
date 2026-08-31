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
	"path/filepath"
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

	command := strings.ToLower(arguments[0])
	var err error
	switch command {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "mmwcli %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "check":
		err = runCheck(arguments[1:], stdout, stderr)
	case "probe":
		err = runProbe(arguments[1:], stdout, stderr)
	case "capture":
		err = runCapture(arguments[1:], stdout, stderr)
	case "stream":
		err = runStream(arguments[1:], stdout, stderr)
	case "setup":
		err = runSetup(arguments[1:], stdout, stderr)
	case "camera":
		err = runCamera(arguments[1:], stdout, stderr)
	default:
		err = usageError{message: "unknown command: " + arguments[0]}
	}
	if err == nil {
		return 0
	}
	return commandExitCode(command, err, stderr)
}

func commandExitCode(command string, err error, stderr io.Writer) int {
	if usage, ok := errors.AsType[usageError](err); ok {
		fmt.Fprintln(stderr, "argument error:", usage.Error())
		fmt.Fprintln(stderr, "run mmwcli help for usage")
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if (command == "capture" || command == "stream") && cancellationOnly(err) {
		fmt.Fprintf(stderr, "%s cancelled; cleanup completed\n", command)
		return 130
	}
	fmt.Fprintln(stderr, "failed:", err)
	return 4
}

func runCheck(arguments []string, stdout, stderr io.Writer) error {
	options, err := parseCaptureOptions(
		"check",
		"mmwcli check RADAR_CFG --setup SETUP --frames N [--camera DEVICE | --radar-only]",
		arguments,
		false,
		stderr,
	)
	if err != nil {
		return err
	}
	setup, err := loadSetup(options.setupPath)
	if err != nil {
		return err
	}
	cameraConfig, err := setup.cameraConfig(options.camera, options.radarOnly)
	if err != nil {
		return err
	}
	return checkCapture(captureRequest{
		ConfigPath: options.configPath,
		Setup:      setup,
		Camera:     cameraConfig,
		Frames:     options.frames,
	}, stdout)
}

func runCapture(arguments []string, stdout, stderr io.Writer) error {
	options, err := parseCaptureOptions(
		"capture",
		"mmwcli capture RADAR_CFG TAKE.capture --setup SETUP --frames N [--camera DEVICE | --radar-only] [--control-stdin]",
		arguments,
		true,
		stderr,
	)
	if err != nil {
		return err
	}
	release, err := acquireHardwareLock(options.setupPath)
	if err != nil {
		return err
	}
	defer release()
	setup, err := loadSetup(options.setupPath)
	if err != nil {
		return err
	}
	cameraConfig, err := setup.cameraConfig(options.camera, options.radarOnly)
	if err != nil {
		return err
	}
	var control io.Reader
	if options.controlStdin {
		control = os.Stdin
	}
	return capture(captureRequest{
		ConfigPath: options.configPath,
		OutputPath: options.outputPath,
		Setup:      setup,
		Camera:     cameraConfig,
		Frames:     options.frames,
		Control:    control,
	}, stdout, stderr)
}

func runStream(arguments []string, stdout, stderr io.Writer) error {
	options, err := parseStreamOptions(arguments, stderr)
	if err != nil {
		return err
	}
	release, err := acquireHardwareLock(options.setupPath)
	if err != nil {
		return err
	}
	defer release()
	setup, err := loadSetup(options.setupPath)
	if err != nil {
		return err
	}
	return stream(streamRequest{ConfigPath: options.configPath, Setup: setup}, os.Stdin, stdout, stderr)
}

type captureOptions struct {
	configPath   string
	outputPath   string
	setupPath    string
	camera       string
	frames       uint16
	radarOnly    bool
	controlStdin bool
}

type streamOptions struct {
	configPath string
	setupPath  string
}

func parseStreamOptions(arguments []string, stderr io.Writer) (streamOptions, error) {
	const synopsis = "mmwcli stream RADAR_CFG --setup SETUP"
	flags := newCommandFlagSet("stream", stderr, synopsis)
	setupPath := flags.String("setup", "", "hardware setup JSON")
	if len(arguments) != 0 && isHelp(arguments[0]) {
		return streamOptions{}, parseCommandFlags(flags, arguments)
	}
	if len(arguments) < 1 || arguments[0] == "" || strings.HasPrefix(arguments[0], "-") {
		return streamOptions{}, usageError{message: synopsis}
	}
	if err := parseCommandFlags(flags, arguments[1:]); err != nil {
		return streamOptions{}, err
	}
	if flags.NArg() != 0 {
		return streamOptions{}, usageError{message: "unexpected stream arguments: " + strings.Join(flags.Args(), " ")}
	}
	if strings.TrimSpace(*setupPath) == "" {
		return streamOptions{}, usageError{message: "--setup is required"}
	}
	return streamOptions{configPath: arguments[0], setupPath: *setupPath}, nil
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
	setupPath := flags.String("setup", "", "hardware setup JSON")
	frames := flags.Int("frames", 0, "finite radar frame count")
	cameraDevice := flags.String("camera", "", "camera ID from camera list")
	radarOnly := flags.Bool("radar-only", false, "capture radar without a camera")
	controlStdin := flags.Bool("control-stdin", false, "accept exact stop command or EOF on stdin")
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
	if strings.TrimSpace(*setupPath) == "" {
		return captureOptions{}, usageError{message: "--setup is required"}
	}
	if *radarOnly && *cameraDevice != "" {
		return captureOptions{}, usageError{
			message: "--camera cannot be used with --radar-only",
		}
	}
	if !wantOutput && *controlStdin {
		return captureOptions{}, usageError{message: "--control-stdin is only valid for capture"}
	}
	if wantOutput && !strings.HasSuffix(strings.ToLower(filepath.Clean(arguments[1])), ".capture") {
		return captureOptions{}, usageError{message: "capture output must end in .capture"}
	}
	options := captureOptions{
		configPath: arguments[0], setupPath: *setupPath, camera: *cameraDevice,
		frames: uint16(*frames), radarOnly: *radarOnly, controlStdin: *controlStdin,
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
		flags := newCommandFlagSet("camera list", stderr, "mmwcli camera list")
		if err := parseCommandFlags(flags, arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return usageError{message: "unexpected arguments: " + strings.Join(flags.Args(), " ")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		devices, err := camera.List(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(struct {
			Devices []camera.Device `json:"devices"`
		}{Devices: devices})
	case "preview":
		setupPath, cameraDevice, err := parseCameraPreview(arguments[1:], stderr)
		if err != nil {
			return err
		}
		release, err := acquireHardwareLock(setupPath)
		if err != nil {
			return err
		}
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		setup, err := loadSetup(setupPath)
		if err != nil {
			return err
		}
		configured, err := setup.cameraConfig(cameraDevice, false)
		if err != nil {
			return err
		}
		image, err := camera.Preview(ctx, *configured)
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

func parseCameraPreview(arguments []string, stderr io.Writer) (string, string, error) {
	const synopsis = "mmwcli camera preview --setup SETUP --camera ID"
	flags := newCommandFlagSet("camera preview", stderr, synopsis)
	setupPath := flags.String("setup", "", "hardware setup JSON")
	cameraDevice := flags.String("camera", "", "camera ID from camera list")
	if err := parseCommandFlags(flags, arguments); err != nil {
		return "", "", err
	}
	if flags.NArg() != 0 {
		return "", "", usageError{message: "unexpected arguments: " + strings.Join(flags.Args(), " ")}
	}
	if strings.TrimSpace(*setupPath) == "" {
		return "", "", usageError{message: "--setup is required"}
	}
	if strings.TrimSpace(*cameraDevice) == "" {
		return "", "", usageError{message: "--camera is required"}
	}
	return *setupPath, *cameraDevice, nil
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
	fmt.Fprintln(writer, "  mmwcli setup show SETUP")
	fmt.Fprintln(writer, "  mmwcli setup mount SETUP --height M --pitch 90")
	fmt.Fprintln(writer, "  mmwcli setup roi SETUP --min-forward M --max-forward M --min-lateral M --max-lateral M --min-up M --max-up M")
	fmt.Fprintln(writer, "  mmwcli probe --setup SETUP [--camera DEVICE]")
	fmt.Fprintln(writer, "  mmwcli check RADAR_CFG --setup SETUP --frames N [--camera DEVICE | --radar-only]")
	fmt.Fprintln(writer, "  mmwcli capture RADAR_CFG TAKE.capture --setup SETUP --frames N [--camera DEVICE | --radar-only] [--control-stdin]")
	fmt.Fprintln(writer, "  mmwcli stream RADAR_CFG --setup SETUP")
	fmt.Fprintln(writer, "  mmwcli camera list")
	fmt.Fprintln(writer, "  mmwcli camera preview --setup SETUP --camera ID")
	fmt.Fprintln(writer, "  mmwcli version")
}

func printCameraHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  mmwcli camera list")
	fmt.Fprintln(writer, "  mmwcli camera preview --setup SETUP --camera ID")
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

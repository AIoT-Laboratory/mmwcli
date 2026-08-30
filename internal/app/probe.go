package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"mmwcli/internal/camera"
	"mmwcli/internal/d2xx"
	"mmwcli/internal/dca"
	"mmwcli/internal/iwr6843"
)

const cameraProbeTimeout = 3 * time.Second

type probeOptions struct {
	setupPath string
	camera    string
}

type probeBackend struct {
	d2xx   func(string) error
	radar  func(context.Context, string, iwr6843.Selectors) error
	dca    func(context.Context, dca.Options) error
	camera func(context.Context, camera.Config) ([]byte, error)
}

func runProbe(arguments []string, stdout, stderr io.Writer) error {
	options, err := parseProbeOptions(arguments, stderr)
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
	var selectedCamera *camera.Config
	if options.camera != "" {
		selectedCamera, err = setup.cameraConfig(options.camera, false)
		if err != nil {
			return err
		}
	}
	ctx, cancel := hardwareSignalContext()
	defer cancel()
	if err := probeHardware(ctx, setup, selectedCamera, defaultProbeBackend()); err != nil {
		return err
	}
	components := "IWR6843 SOP2/Enhanced COM, D2XX A/B/C/D, DCA1000 SystemAlive"
	if selectedCamera != nil {
		components += ", camera"
	}
	fmt.Fprintln(stdout, "hardware probe passed: "+components)
	return nil
}

func parseProbeOptions(arguments []string, stderr io.Writer) (probeOptions, error) {
	const synopsis = "mmwcli probe --setup SETUP [--camera DEVICE]"
	flags := newCommandFlagSet("probe", stderr, synopsis)
	setupPath := flags.String("setup", "", "hardware setup JSON")
	cameraDevice := flags.String("camera", "", "optional camera ID from camera list")
	if err := parseCommandFlags(flags, arguments); err != nil {
		return probeOptions{}, err
	}
	if flags.NArg() != 0 {
		return probeOptions{}, usageError{message: "unexpected probe arguments: " + strings.Join(flags.Args(), " ")}
	}
	if strings.TrimSpace(*setupPath) == "" {
		return probeOptions{}, usageError{message: "--setup is required"}
	}
	return probeOptions{setupPath: *setupPath, camera: *cameraDevice}, nil
}

func defaultProbeBackend() probeBackend {
	return probeBackend{
		d2xx:   probeD2XX,
		radar:  iwr6843.Probe,
		dca:    probeDCA,
		camera: camera.Preview,
	}
}

func probeHardware(
	ctx context.Context,
	setup setupConfig,
	selectedCamera *camera.Config,
	backend probeBackend,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if backend.d2xx == nil || backend.radar == nil || backend.dca == nil ||
		(selectedCamera != nil && backend.camera == nil) {
		return errors.New("hardware probe backend is incomplete")
	}
	if err := backend.d2xx(setup.Radar.D2XX); err != nil {
		return fmt.Errorf("probe D2XX A/B/C/D: %w", err)
	}
	selectors, err := buildSelectors(setup.Radar.D2XX)
	if err != nil {
		return err
	}
	if err := backend.radar(ctx, setup.Radar.Port, selectors); err != nil {
		return fmt.Errorf("probe IWR6843 SOP2/Enhanced COM: %w", err)
	}
	dcaSetup, err := dcaForSetup(setup)
	if err != nil {
		return err
	}
	dcaSetup.control.ControlBindAddress = net.ParseIP(setup.DCA.Host).To4()
	if err := backend.dca(ctx, dcaSetup.control); err != nil {
		return fmt.Errorf("probe DCA1000 SystemAlive: %w", err)
	}
	if selectedCamera != nil {
		cameraContext, cancel := context.WithTimeout(ctx, cameraProbeTimeout)
		_, err = backend.camera(cameraContext, *selectedCamera)
		cancel()
		if err != nil {
			return fmt.Errorf("probe camera: %w", err)
		}
	}
	return nil
}

func probeD2XX(description string) error {
	library, err := d2xx.Load()
	if err != nil {
		return err
	}
	return probeD2XXInterfaces(
		description,
		func(selector d2xx.Selector) (io.Closer, error) { return library.Open(selector) },
		library.Close,
	)
}

func probeD2XXInterfaces(
	description string,
	open func(d2xx.Selector) (io.Closer, error),
	closeLibrary func() error,
) (resultErr error) {
	defer func() { resultErr = errors.Join(resultErr, closeLibrary()) }()
	for _, suffix := range []string{" A", " B", " C", " D"} {
		device, err := open(d2xx.Selector{Description: description + suffix})
		if err != nil {
			return fmt.Errorf("open interface%s: %w", suffix, err)
		}
		if device == nil {
			return fmt.Errorf("open interface%s returned a nil device", suffix)
		}
		if err := device.Close(); err != nil {
			return fmt.Errorf("close interface%s: %w", suffix, err)
		}
	}
	return nil
}

func probeDCA(ctx context.Context, options dca.Options) (resultErr error) {
	client, err := dca.Dial(options)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	response, err := client.Ping(ctx)
	if err != nil {
		return err
	}
	if response.Status != 0 {
		return &dca.StatusError{Command: response.Command, Status: response.Status}
	}
	return nil
}

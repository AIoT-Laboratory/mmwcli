package app

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"mmwcli/internal/camera"
	"mmwcli/internal/d2xx"
	"mmwcli/internal/dca"
	"mmwcli/internal/iwr6843"
	"mmwcli/internal/radar"
	"mmwcli/internal/session"
	"mmwcli/internal/take"
)

type captureRequest struct {
	ConfigPath string
	OutputPath string
	Setup      setupConfig
	Camera     *camera.Config
	Frames     uint16
	Control    io.Reader
}

type preparedRun struct {
	radar     loadedPlan
	assets    iwr6843.Assets
	link      iwr6843.Plan
	dca       dcaConfig
	session   session.Prepared
	selectors iwr6843.Selectors
}

func checkCapture(request captureRequest, stdout io.Writer) error {
	_, err := prepareCapture(request, stdout)
	return err
}

func capture(request captureRequest, stdout, stderr io.Writer) error {
	ready, err := prepareCapture(request, stdout)
	if err != nil {
		return err
	}
	stats, err := captureHardware(request, ready, stdout, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, summarizeCapture(stats))
	return nil
}

func prepareCapture(
	request captureRequest,
	stdout io.Writer,
) (preparedRun, error) {
	if request.Frames == 0 {
		return preparedRun{}, usageError{message: "--frames must be in 1..65535"}
	}
	loaded, err := loadPlan(request.ConfigPath, request.Frames)
	if err != nil {
		return preparedRun{}, err
	}
	if loaded.plan.NumberOfFrames != request.Frames {
		return preparedRun{}, errors.New("capture plan must be finite and match --frames")
	}
	dcaSetup, err := dcaForSetup(request.Setup)
	if err != nil {
		return preparedRun{}, err
	}
	preparedSession, err := session.Prepare(
		loaded.plan,
		dcaSetup.fpga,
		dcaSetup.receiver,
		dcaSetup.delay,
		dcaSetup.control.Timeout,
	)
	if err != nil {
		return preparedRun{}, usageError{message: err.Error()}
	}
	ready, err := prepareHardware(request.Setup, loaded, dcaSetup, preparedSession)
	if err != nil {
		return preparedRun{}, err
	}
	if request.Camera != nil {
		if _, err := exec.LookPath(camera.Executable); err != nil {
			return preparedRun{}, fmt.Errorf(
				"find camera executable %q: %w",
				camera.Executable,
				err,
			)
		}
	}
	printFiniteCapture(stdout, loaded.plan)
	fmt.Fprintln(stdout, "capture check passed (offline; no hardware accessed)")
	return ready, nil
}

func prepareHardware(
	setup setupConfig,
	loaded loadedPlan,
	dcaSetup dcaConfig,
	preparedSession session.Prepared,
) (preparedRun, error) {
	link, err := iwr6843.BuildPlan(loaded.plan)
	if err != nil {
		return preparedRun{}, err
	}
	assets, err := iwr6843.CheckAssets(setup.bssPath, setup.mssPath)
	if err != nil {
		return preparedRun{}, err
	}
	library, err := d2xx.Load()
	if err != nil {
		return preparedRun{}, err
	}
	if err := library.Close(); err != nil {
		return preparedRun{}, err
	}
	selectors, err := buildSelectors(setup.Radar.D2XX)
	if err != nil {
		return preparedRun{}, err
	}
	return preparedRun{
		radar: loaded, assets: assets, link: link, dca: dcaSetup,
		session: preparedSession, selectors: selectors,
	}, nil
}

func loadPlan(path string, frameCount uint16) (loadedPlan, error) {
	snapshot, err := readRadarConfig(path)
	if err != nil {
		return loadedPlan{}, err
	}
	snapshot, err = radar.SetFrameCount(snapshot, frameCount)
	if err != nil {
		return loadedPlan{}, err
	}
	plan, err := radar.BuildPlan(snapshot)
	if err != nil {
		return loadedPlan{}, err
	}
	return loadedPlan{
		plan:   plan,
		source: snapshot,
	}, nil
}

func buildSelectors(description string) (iwr6843.Selectors, error) {
	if description == "" {
		return iwr6843.Selectors{}, errors.New("D2XX description is required")
	}
	selectors := iwr6843.Selectors{
		SPI: d2xx.Selector{Description: description + " A"},
		IRQ: d2xx.Selector{Description: description + " B"},
	}
	if err := selectors.SPI.Validate(); err != nil {
		return iwr6843.Selectors{}, fmt.Errorf("invalid D2XX SPI selector: %w", err)
	}
	if err := selectors.IRQ.Validate(); err != nil {
		return iwr6843.Selectors{}, fmt.Errorf("invalid D2XX IRQ selector: %w", err)
	}
	return selectors, nil
}

func captureHardware(
	request captureRequest,
	ready preparedRun,
	stdout, stderr io.Writer,
) (stats dca.CaptureStats, resultErr error) {
	ctx, cancel := hardwareSignalContext()
	defer cancel()
	if request.Control != nil {
		go watchManagedStop(request.Control, cancel)
	}
	cameraConfig := request.Camera
	captureOutput, err := take.New(ctx, cancel, take.Config{
		Output:      request.OutputPath,
		RadarConfig: ready.radar.source,
		Plan:        ready.radar.plan,
		Setup:       setupSnapshot(request.Setup, ready.assets, cameraConfig),
		Camera:      cameraConfig,
	}, stderr)
	if err != nil {
		return stats, err
	}
	defer func() { resultErr = errors.Join(resultErr, captureOutput.Close()) }()

	dcaClient, err := dca.Dial(ready.dca.control)
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = closeCaptureClient(
			resultErr,
			captureOutput.RadarOutput().Committed(),
			"DCA1000",
			dcaClient,
		)
	}()
	controller, err := iwr6843.Open(ctx, iwr6843.Options{
		EnhancedPort: request.Setup.Radar.Port,
		Assets:       ready.assets,
		Selectors:    ready.selectors,
		Plan:         ready.link,
	})
	if err != nil {
		return stats, err
	}
	defer func() {
		resultErr = closeCaptureClient(
			resultErr,
			captureOutput.RadarOutput().Committed(),
			"IWR6843",
			controller,
		)
	}()

	preparedSession := ready.session
	preparedSession.Participant = captureOutput
	preparedSession.Log = sessionLog(stderr, "capture")
	stats, err = session.Run(
		ctx,
		controller,
		dcaClient,
		func(config dca.ReceiverConfig) (session.Receiver, error) {
			return dca.NewReceiver(config)
		},
		ready.radar.plan,
		captureOutput.RadarOutput(),
		preparedSession,
	)
	if err != nil {
		return stats, err
	}
	if !captureOutput.RadarOutput().Committed() {
		return stats, errors.New("radar completed without publishing its capture")
	}
	if err := captureOutput.Publish(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}

func setupSnapshot(setup setupConfig, assets iwr6843.Assets, selectedCamera *camera.Config) take.SetupSnapshot {
	firmware := func(file iwr6843.File) take.SetupFile {
		return take.SetupFile{
			Name: filepath.Base(file.Path), Bytes: uint64(file.Size), SHA256: strings.ToLower(file.SHA256),
		}
	}
	var cameraSnapshot *camera.Config
	if selectedCamera != nil {
		value := *selectedCamera
		cameraSnapshot = &value
	}
	return take.SetupSnapshot{
		Schema: take.SetupSchema,
		Radar: take.SetupRadar{
			Model: "iwr6843", Revision: "es2", Port: setup.Radar.Port,
			BSS: firmware(assets.BSS), MSS: firmware(assets.MSS), D2XX: setup.Radar.D2XX,
		},
		DCA: take.SetupDCA{
			Host: setup.DCA.Host, Device: setup.DCA.Device, DelayUS: setup.DCA.DelayUS,
		},
		Mount:  take.SetupMount{HeightM: setup.Mount.HeightM, PitchDeg: setup.Mount.PitchDeg},
		Camera: cameraSnapshot,
	}
}

package app

import (
	"errors"
	"fmt"
	"io"
	"os/exec"

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
	Rig        rigConfig
	Frames     uint16
	RadarOnly  bool
}

type preparedCapture struct {
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
) (preparedCapture, error) {
	if request.Frames == 0 {
		return preparedCapture{}, usageError{message: "--frames must be in 1..65535"}
	}
	loaded, err := loadPlan(
		request.ConfigPath,
		&request.Frames,
	)
	if err != nil {
		return preparedCapture{}, err
	}
	if loaded.plan.NumberOfFrames != request.Frames {
		return preparedCapture{}, errors.New("capture plan must be finite and match --frames")
	}
	dcaSetup, err := dcaForRig(request.Rig)
	if err != nil {
		return preparedCapture{}, err
	}
	preparedSession, err := session.Prepare(
		loaded.plan,
		dcaSetup.fpga,
		dcaSetup.receiver,
		dcaSetup.delay,
		dcaSetup.control.Timeout,
	)
	if err != nil {
		return preparedCapture{}, usageError{message: err.Error()}
	}
	link, err := iwr6843.BuildPlan(loaded.plan)
	if err != nil {
		return preparedCapture{}, err
	}
	assets, err := iwr6843.CheckAssets(request.Rig.BSS, request.Rig.MSS)
	if err != nil {
		return preparedCapture{}, err
	}
	library, err := d2xx.Load()
	if err != nil {
		return preparedCapture{}, err
	}
	if err := library.Close(); err != nil {
		return preparedCapture{}, err
	}
	selectors, err := buildSelectors(request.Rig.D2XX)
	if err != nil {
		return preparedCapture{}, err
	}
	if !request.RadarOnly {
		if request.Rig.Camera == nil {
			return preparedCapture{}, errors.New("camera is required unless --radar-only is set")
		}
		if _, err := exec.LookPath(request.Rig.Camera.Command[0]); err != nil {
			return preparedCapture{}, fmt.Errorf(
				"find camera executable %q: %w",
				request.Rig.Camera.Command[0],
				err,
			)
		}
	}
	printFiniteCapture(stdout, loaded.plan)
	fmt.Fprintln(stdout, "capture check passed (offline; no hardware accessed)")
	return preparedCapture{
		radar:     loaded,
		assets:    assets,
		link:      link,
		dca:       dcaSetup,
		session:   preparedSession,
		selectors: selectors,
	}, nil
}

func loadPlan(path string, frameCount *uint16) (loadedPlan, error) {
	snapshot, err := readRadarConfig(path)
	if err != nil {
		return loadedPlan{}, err
	}
	if frameCount != nil {
		snapshot, err = radar.SetFrameCount(snapshot, *frameCount)
		if err != nil {
			return loadedPlan{}, err
		}
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
	ready preparedCapture,
	stdout, stderr io.Writer,
) (stats dca.CaptureStats, resultErr error) {
	ctx, cancel := hardwareSignalContext()
	defer cancel()
	var cameraConfig *camera.Config
	if !request.RadarOnly {
		cameraConfig = &camera.Config{
			Command:  append([]string(nil), request.Rig.Camera.Command...),
			MaxBytes: request.Rig.Camera.MaxBytes,
		}
	}
	captureOutput, err := take.New(ctx, cancel, take.Config{
		Output:       request.OutputPath,
		RadarConfig:  ready.radar.source,
		Plan:         ready.radar.plan,
		RadarHeightM: request.Rig.HeightM,
		Camera:       cameraConfig,
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
		EnhancedPort: request.Rig.Port,
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
	preparedSession.Log = func(message string) { fmt.Fprintln(stdout, "[capture] "+message) }
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

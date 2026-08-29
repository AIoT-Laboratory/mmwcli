package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"mmwcli/internal/dca"
	"mmwcli/internal/iwr6843"
	"mmwcli/internal/session"
)

type streamRequest struct {
	ConfigPath string
	Rig        rigConfig
}

type streamHeader struct {
	FrameBytes int64   `json:"frame_bytes"`
	PeriodNS   int64   `json:"period_ns"`
	HeightM    float64 `json:"height_m"`
	TiltDeg    float64 `json:"tilt_deg"`
}

func stream(request streamRequest, control io.Reader, stdout, stderr io.Writer) error {
	ready, err := prepareStream(request, stderr)
	if err != nil {
		return err
	}
	header := streamHeader{
		FrameBytes: ready.radar.plan.BytesPerFrame,
		PeriodNS:   int64(ready.radar.plan.FramePeriod),
		HeightM:    request.Rig.HeightM,
		TiltDeg:    request.Rig.TiltDeg,
	}
	if err := json.NewEncoder(stdout).Encode(header); err != nil {
		return fmt.Errorf("write stream header: %w", err)
	}
	return streamHardware(request, ready, control, stdout, stderr)
}

func prepareStream(request streamRequest, stderr io.Writer) (preparedRun, error) {
	continuous := uint16(0)
	loaded, err := loadPlan(request.ConfigPath, continuous)
	if err != nil {
		return preparedRun{}, err
	}
	if loaded.plan.NumberOfFrames != 0 || loaded.plan.ExpectedBytes != 0 {
		return preparedRun{}, errors.New("stream plan must use frameCfg numFrames=0")
	}
	dcaSetup, err := dcaForRig(request.Rig)
	if err != nil {
		return preparedRun{}, err
	}
	preparedSession, err := session.PrepareStream(
		loaded.plan,
		dcaSetup.fpga,
		dcaSetup.receiver,
		dcaSetup.delay,
		dcaSetup.control.Timeout,
	)
	if err != nil {
		return preparedRun{}, usageError{message: err.Error()}
	}
	ready, err := prepareHardware(request.Rig, loaded, dcaSetup, preparedSession)
	if err != nil {
		return preparedRun{}, err
	}
	fmt.Fprintf(
		stderr,
		"radar stream: IWR6843 period=%s bytes/frame=%d height=%.3fm tilt=%.0fdeg\n",
		loaded.plan.FramePeriod,
		loaded.plan.BytesPerFrame,
		request.Rig.HeightM,
		request.Rig.TiltDeg,
	)
	return ready, nil
}

func streamHardware(
	request streamRequest,
	ready preparedRun,
	control io.Reader,
	stdout, stderr io.Writer,
) (resultErr error) {
	ctx, cancel := hardwareSignalContext()
	defer cancel()
	go watchStreamStop(control, cancel)
	frames, err := dca.NewFrameWriter(ready.radar.plan.BytesPerFrame, stdout)
	if err != nil {
		return err
	}
	dcaClient, err := dca.Dial(ready.dca.control)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = closeCaptureClient(resultErr, false, "DCA1000", dcaClient)
	}()
	controller, err := iwr6843.Open(ctx, iwr6843.Options{
		EnhancedPort: request.Rig.Port,
		Assets:       ready.assets,
		Selectors:    ready.selectors,
		Plan:         ready.link,
	})
	if err != nil {
		return err
	}
	defer func() {
		resultErr = closeCaptureClient(resultErr, false, "IWR6843", controller)
	}()

	preparedSession := ready.session
	preparedSession.Log = func(message string) { fmt.Fprintln(stderr, "[stream] "+message) }
	_, err = session.Stream(
		ctx,
		controller,
		dcaClient,
		func(config dca.ReceiverConfig) (session.Receiver, error) {
			return dca.NewReceiver(config)
		},
		ready.radar.plan,
		frames,
		preparedSession,
	)
	return err
}

func watchStreamStop(control io.Reader, cancel context.CancelFunc) {
	defer cancel()
	if control == nil {
		return
	}
	scanner := bufio.NewScanner(control)
	for scanner.Scan() {
		if scanner.Text() == "stop" {
			return
		}
	}
}

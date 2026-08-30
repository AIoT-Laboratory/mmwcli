package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

func PrepareStream(
	plan radar.Plan,
	fpga dca.FPGAConfig,
	receiver dca.ReceiverConfig,
	packetDelay int,
	commandTimeout time.Duration,
) (Prepared, error) {
	if commandTimeout <= 0 || receiver.FirstPacketTimeout <= 0 || receiver.IdleTimeout <= 0 {
		return Prepared{}, errors.New("stream control and receiver timeouts must be positive")
	}
	if plan.BytesPerFrame <= 0 || plan.FramePeriod <= 0 ||
		plan.NumberOfFrames != 0 || plan.ExpectedBytes != 0 {
		return Prepared{}, errors.New("radar stream plan must use frameCfg numFrames=0")
	}
	if err := validateSetup(plan, fpga, packetDelay); err != nil {
		return Prepared{}, err
	}
	receiver.MaxOutputBytes = math.MaxInt64
	receiver.ExpectedOutputBytes = 0
	receiver.CadenceFrameBytes = 0
	receiver.CadenceFramePeriod = 0
	receiver.RejectMalformed = true
	receiver.RequireFreshStart = true
	if err := enforceReceiverTimeouts(plan, &receiver); err != nil {
		return Prepared{}, err
	}
	return Prepared{
		fpga: fpga, receiver: receiver, packetDelay: packetDelay,
		commandTimeout: commandTimeout, timings: defaultRunTimings,
		streaming: true, valid: true,
	}, nil
}

// Stream runs one continuous radar/DCA session until cancellation or a data
// failure. It never creates or publishes a finite capture artifact.
func Stream(
	ctx context.Context,
	radarControl Radar,
	dcaControl DCA,
	newReceiver func(dca.ReceiverConfig) (Receiver, error),
	plan radar.Plan,
	output io.WriterAt,
	prepared Prepared,
) (stats dca.CaptureStats, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if radarControl == nil || dcaControl == nil || newReceiver == nil || output == nil ||
		!prepared.valid || !prepared.streaming || plan.NumberOfFrames != 0 || plan.ExpectedBytes != 0 {
		return stats, errors.New("stream session dependencies are incomplete")
	}
	log := prepared.Log
	if log == nil {
		log = func(string) {}
	}
	state := runState{radar: radarControl, dca: dcaControl, newReceiver: newReceiver}
	receiverDone := false
	var err error

	defer func() {
		cleanupErr := cleanup(
			radarControl,
			dcaControl,
			state.receiver,
			state.receiverCancel,
			state.receiverStarted && !receiverDone,
			state.radarRunning,
			false,
			state.dcaUsed,
			state.dcaRecording,
			prepared,
			log,
		)
		if state.receiver != nil {
			stats = state.receiver.Stats()
		}
		if cancellationErr := ctx.Err(); cancellationErr != nil {
			resultErr = errors.Join(resultErr, cancellationErr)
		}
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, &CleanupError{Err: cleanupErr})
		}
	}()

	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if err := state.configure(ctx, plan, prepared, log); err != nil {
		return stats, err
	}
	if err := state.armData(ctx, output, prepared, log); err != nil {
		return stats, err
	}

	if err := ctx.Err(); err != nil {
		return stats, err
	}
	state.radarRunning = true
	if _, err := radarControl.Start(ctx); err != nil {
		return stats, fmt.Errorf("start radar: %w", err)
	}
	log("radar started")

	if err := state.receiver.WaitFirst(ctx); err != nil {
		receiverDone = ctx.Err() == nil
		return stats, err
	}
	stats, err = state.receiver.Wait(ctx)
	receiverDone = ctx.Err() == nil
	if err != nil {
		return stats, err
	}
	return stats, errors.New("continuous DCA stream ended unexpectedly")
}

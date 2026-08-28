// Package session coordinates one radar CLI and one DCA1000 capture without
// hiding state-changing retries.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

type Radar interface {
	Verify(context.Context) (string, error)
	Stop(context.Context) (string, error)
	Apply(context.Context, radar.Plan) error
	Start(context.Context) (string, error)
	AwaitEnd(context.Context) (string, error)
	StartInterval() (lower, upper time.Time, err error)
}

type DCA interface {
	Execute(context.Context, dca.Command, []byte) (dca.Response, error)
	Configure(context.Context, dca.FPGAConfig, int) (dca.ConfigurationResponses, error)
	Start(context.Context) (dca.Response, error)
	Stop(context.Context) (dca.Response, error)
	TakeStatuses() []dca.Response
	DrainStatuses(context.Context, time.Duration) ([]dca.Response, error)
}

type Receiver interface {
	Start(context.Context, io.WriterAt) error
	WaitFirst(context.Context) error
	Wait(context.Context) (dca.CaptureStats, error)
	Stats() dca.CaptureStats
	Close() error
}

// Participant coordinates one already-created auxiliary sensor with the
// radar/DCA lifecycle. Ready/process setup belongs to the caller. Arm runs
// after radar and DCA configuration, Start runs immediately before the radar
// start command, and Finish runs once after hardware cleanup. A true complete
// value permits the participant to stop and validate its finite data; false
// requires cancellation and cleanup.
type Participant interface {
	Arm(context.Context) error
	Start(context.Context) error
	Finish(context.Context, bool) error
	SetRadarStart(lower, upper time.Time)
}

// CleanupError marks a failure that occurred while converging hardware or
// closing capture resources. Callers must not downgrade a cancellation joined
// with this error to a successful exit-130 cleanup.
type CleanupError struct{ Err error }

func (e *CleanupError) Error() string       { return "capture cleanup failed: " + e.Err.Error() }
func (e *CleanupError) Unwrap() error       { return e.Err }
func (e *CleanupError) CleanupFailed() bool { return true }

type finiteCaptureDeadlineError struct {
	maximum time.Duration
}

func (e *finiteCaptureDeadlineError) Error() string {
	return fmt.Sprintf("finite-frame data exceeded planned maximum duration %s", e.maximum)
}

func (e *finiteCaptureDeadlineError) Unwrap() error { return context.DeadlineExceeded }

type runTimings struct {
	drain        time.Duration
	controlDrain time.Duration
	controlQuiet time.Duration
	radarCleanup time.Duration
	participant  time.Duration
}

var defaultRunTimings = runTimings{
	drain: 3 * time.Second, controlDrain: 500 * time.Millisecond,
	controlQuiet: 50 * time.Millisecond, radarCleanup: 3 * time.Second,
	participant: 5 * time.Second,
}

// Prepared is the fully validated fixed IWR6843/DCA1000 capture configuration.
// Participant and Log are lifecycle collaborators, not capture policy.
type Prepared struct {
	Participant Participant
	Log         func(string)

	fpga                     dca.FPGAConfig
	receiver                 dca.ReceiverConfig
	packetDelay              int
	commandTimeout           time.Duration
	maximumStreamingDuration time.Duration
	timings                  runTimings
	valid                    bool
}

func Prepare(
	plan radar.Plan,
	fpga dca.FPGAConfig,
	receiver dca.ReceiverConfig,
	packetDelay int,
	commandTimeout time.Duration,
) (Prepared, error) {
	if commandTimeout <= 0 || receiver.FirstPacketTimeout <= 0 || receiver.IdleTimeout <= 0 {
		return Prepared{}, errors.New("capture control and receiver timeouts must be positive")
	}
	if plan.BytesPerFrame <= 0 || plan.FramePeriod <= 0 ||
		plan.NumberOfFrames == 0 || plan.ExpectedBytes <= 0 {
		return Prepared{}, errors.New("radar capture plan has invalid finite frame accounting")
	}
	if err := dca.ValidateRawCaptureFPGAConfig(fpga); err != nil {
		return Prepared{}, err
	}
	if _, err := dca.BuildRecordConfig(packetDelay); err != nil {
		return Prepared{}, err
	}
	if plan.ExpectedDCADataFormat != fpga.DataFormat {
		return Prepared{}, fmt.Errorf(
			"radar adcCfg requires DCA data-format=%d, configured=%d",
			plan.ExpectedDCADataFormat, fpga.DataFormat,
		)
	}
	if !plan.HardwareLVDSEnabled {
		return Prepared{}, errors.New("radar configuration does not enable hardware LVDS")
	}
	if plan.ExpectedBytes%plan.BytesPerFrame != 0 ||
		plan.ExpectedBytes/plan.BytesPerFrame != int64(plan.NumberOfFrames) {
		return Prepared{}, errors.New("radar capture plan has inconsistent frame byte accounting")
	}
	if receiver.MaxOutputBytes < plan.ExpectedBytes {
		return Prepared{}, fmt.Errorf(
			"DCA maximum output size %d is smaller than capture size %d",
			receiver.MaxOutputBytes, plan.ExpectedBytes,
		)
	}
	receiver.MaxOutputBytes = plan.ExpectedBytes
	receiver.ExpectedOutputBytes = plan.ExpectedBytes
	receiver.CadenceFrameBytes = plan.BytesPerFrame
	receiver.CadenceFramePeriod = plan.FramePeriod
	requiredIdleTimeout, err := MinimumReceiverIdleTimeout(plan)
	if err != nil {
		return Prepared{}, err
	}
	if receiver.IdleTimeout < requiredIdleTimeout {
		receiver.IdleTimeout = requiredIdleTimeout
	}
	requiredFirstPacketTimeout, err := MinimumFirstPacketTimeout(plan)
	if err != nil {
		return Prepared{}, err
	}
	if receiver.FirstPacketTimeout < requiredFirstPacketTimeout {
		receiver.FirstPacketTimeout = requiredFirstPacketTimeout
	}
	maximum, err := plan.MaxDuration(receiver.IdleTimeout)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		fpga: fpga, receiver: receiver, packetDelay: packetDelay,
		commandTimeout: commandTimeout, maximumStreamingDuration: maximum,
		timings: defaultRunTimings, valid: true,
	}, nil
}

func Run(
	ctx context.Context,
	radarControl Radar,
	dcaControl DCA,
	newReceiver func(dca.ReceiverConfig) (Receiver, error),
	plan radar.Plan,
	output capturefile.Output,
	prepared Prepared,
) (stats dca.CaptureStats, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if radarControl == nil || dcaControl == nil || newReceiver == nil ||
		!capturefile.IsUsableOutput(output) || !prepared.valid || prepared.Participant == nil {
		return stats, errors.New("capture session dependencies are incomplete")
	}
	outputManagedByLifecycle := false
	defer func() {
		if outputManagedByLifecycle {
			return
		}
		if closeErr := output.Close(); closeErr != nil {
			marked := &CleanupError{Err: closeErr}
			if resultErr == nil {
				resultErr = marked
			} else {
				resultErr = errors.Join(resultErr, marked)
			}
		}
	}()
	log := prepared.Log
	if log == nil {
		log = func(string) {}
	}
	var receiver Receiver
	var receiverCancel context.CancelFunc
	receiverStarted := false
	dcaUsed := false
	dcaRecording := false
	radarMayBeRunning := false
	finiteFrameEndObserved := false
	var radarStartIssuedAt time.Time
	participant := prepared.Participant
	participantActive := false

	outputManagedByLifecycle = true
	defer func() {
		cleanupErr := cleanup(
			radarControl,
			dcaControl,
			receiver,
			receiverCancel,
			receiverStarted,
			radarMayBeRunning,
			finiteFrameEndObserved && ctx.Err() == nil,
			dcaUsed,
			dcaRecording,
			prepared,
			log,
		)
		if receiver != nil {
			stats = receiver.Stats()
		}
		var deadlineErr *finiteCaptureDeadlineError
		if errors.As(resultErr, &deadlineErr) {
			resultErr = fmt.Errorf(
				"%w: coverage expected=%d output=%d missing=%d sequenceGaps=%d discardedBeforeBase=%d",
				resultErr,
				plan.ExpectedBytes,
				stats.OutputBytes,
				stats.MissingBytes,
				stats.SequenceGaps,
				stats.DiscardedBeforeBasePackets,
			)
		}
		if cancellationErr := ctx.Err(); cancellationErr != nil {
			resultErr = errors.Join(resultErr, cancellationErr)
		}
		if resultErr == nil {
			resultErr = validateResult(plan, stats, prepared.receiver.IdleTimeout, radarStartIssuedAt)
		}
		if participantActive {
			participantContext, cancelParticipant := context.WithTimeout(
				context.Background(),
				prepared.timings.participant,
			)
			participantErr := participant.Finish(participantContext, resultErr == nil)
			cancelParticipant()
			if participantErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("finish capture participant: %w", participantErr))
			}
		}
		if resultErr == nil {
			if err := output.Truncate(stats.OutputBytes); err != nil {
				resultErr = err
			} else {
				resultErr = output.CommitContext(ctx)
			}
		}
		if resultErr != nil {
			if closeErr := output.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, &CleanupError{Err: closeErr})
			}
		}
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, &CleanupError{Err: cleanupErr})
		}
	}()

	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if _, err := radarControl.Verify(ctx); err != nil {
		return stats, err
	}
	if _, err := radarControl.Stop(ctx); err != nil {
		return stats, fmt.Errorf("establish stopped radar state: %w", err)
	}
	log("radar stopped")

	dcaUsed = true
	response, err := dcaControl.Stop(ctx)
	if err != nil {
		return stats, fmt.Errorf("establish stopped DCA state: %w", err)
	}
	if err := requireStatus(response); err != nil {
		return stats, err
	}
	log("DCA1000 stopped")

	if _, err := dcaControl.Configure(ctx, prepared.fpga, prepared.packetDelay); err != nil {
		return stats, err
	}
	if err := fatalAsyncError(dcaControl.TakeStatuses()); err != nil {
		return stats, err
	}
	log("DCA1000 configured")

	if err := radarControl.Apply(ctx, plan); err != nil {
		return stats, fmt.Errorf("apply radar configuration: %w", err)
	}
	log("radar configuration applied")

	participantActive = true
	participantContext, cancelParticipant := context.WithTimeout(ctx, prepared.timings.participant)
	err = participant.Arm(participantContext)
	cancelParticipant()
	if err != nil {
		return stats, fmt.Errorf("arm capture participant: %w", err)
	}
	log("capture participant armed")

	receiver, err = newReceiver(prepared.receiver)
	if err != nil {
		return stats, err
	}
	receiverContext, cancelReceiver := context.WithCancel(context.Background())
	receiverCancel = cancelReceiver
	if err := receiver.Start(receiverContext, output); err != nil {
		return stats, err
	}
	receiverStarted = true
	log("DCA1000 data socket armed")

	response, err = dcaControl.Start(ctx)
	if err != nil {
		// Start has already sent the one permitted StopRecord;
		// dcaRecording remains false so cleanup will not send another.
		return stats, err
	}
	if err := requireStatus(response); err != nil {
		return stats, err
	}
	dcaRecording = true
	if err := fatalAsyncError(dcaControl.TakeStatuses()); err != nil {
		return stats, err
	}
	log("DCA1000 recording started")

	if err := ctx.Err(); err != nil {
		return stats, err
	}
	participantContext, cancelParticipant = context.WithTimeout(ctx, prepared.timings.participant)
	err = participant.Start(participantContext)
	cancelParticipant()
	if err != nil {
		return stats, fmt.Errorf("start capture participant: %w", err)
	}
	log("capture participant started")
	radarMayBeRunning = true
	radarStartIssuedAt = time.Now()
	_, err = radarControl.Start(ctx)
	if err != nil {
		return stats, fmt.Errorf("start radar: %w", err)
	}
	log("radar started")

	if err := receiver.WaitFirst(ctx); err != nil {
		return stats, err
	}
	firstPacketAt := receiver.Stats().FirstPacketAt
	if !firstPacketAt.IsZero() && !firstPacketAt.Before(radarStartIssuedAt) {
		lower, upper, intervalErr := resolveStartInterval(
			radarControl,
			radarStartIssuedAt,
			firstPacketAt,
		)
		if intervalErr != nil {
			return stats, intervalErr
		}
		participant.SetRadarStart(lower, upper)
	}
	if firstPacketAt.IsZero() {
		return stats, errors.New("DCA1000 receiver reported a first packet without a timestamp")
	}
	waitContext, cancelWait := context.WithDeadline(ctx, firstPacketAt.Add(prepared.maximumStreamingDuration))
	stats, err = receiver.Wait(waitContext)
	cancelWait()
	if err == nil && ctx.Err() == nil && stats.OutputBytes == plan.ExpectedBytes &&
		validateFiniteFrameTiming(plan, stats, radarStartIssuedAt) == nil {
		finiteFrameEndObserved = true
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return stats, &finiteCaptureDeadlineError{maximum: prepared.maximumStreamingDuration}
	}
	return stats, err
}

func resolveStartInterval(
	radarControl Radar,
	commandLower time.Time,
	firstPacketUpper time.Time,
) (time.Time, time.Time, error) {
	if commandLower.IsZero() || firstPacketUpper.IsZero() || firstPacketUpper.Before(commandLower) {
		return time.Time{}, time.Time{}, errors.New("radar frame-start command interval is invalid")
	}
	lower := commandLower
	upper := firstPacketUpper
	eventLower, eventUpper, err := radarControl.StartInterval()
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("read radar frame-start event interval: %w", err)
	}
	if eventLower.IsZero() || eventUpper.IsZero() || eventUpper.Before(eventLower) {
		return time.Time{}, time.Time{}, errors.New("radar frame-start event interval is invalid")
	}
	if eventLower.After(lower) {
		lower = eventLower
	}
	if eventUpper.Before(upper) {
		upper = eventUpper
	}
	if upper.Before(lower) {
		return time.Time{}, time.Time{}, errors.New(
			"radar frame-start event and DCA first-packet intervals do not overlap",
		)
	}
	return lower, upper, nil
}

func cleanup(
	radarControl Radar,
	dcaControl DCA,
	receiver Receiver,
	receiverCancel context.CancelFunc,
	receiverStarted bool,
	radarMayBeRunning bool,
	finiteFrameEndObserved bool,
	dcaUsed bool,
	dcaRecording bool,
	prepared Prepared,
	log func(string),
) error {
	var failures []error
	if radarMayBeRunning {
		stopContext, cancel := context.WithTimeout(context.Background(), prepared.timings.radarCleanup)
		operation := "stop radar"
		var err error
		if finiteFrameEndObserved {
			operation = "wait for finite radar frame end"
			_, err = radarControl.AwaitEnd(stopContext)
		} else {
			_, err = radarControl.Stop(stopContext)
		}
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s during cleanup: %w", operation, err))
		} else if operation == "wait for finite radar frame end" {
			log("finite radar frame ended during cleanup")
		} else {
			log("radar stopped during cleanup")
		}
	}
	if receiverStarted && receiver != nil && radarMayBeRunning {
		drainContext, cancel := context.WithTimeout(context.Background(), prepared.timings.drain)
		_, err := receiver.Wait(drainContext)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, dca.ErrFirstPacketTimeout) && !errors.Is(err, dca.ErrReceiverClosed) {
			failures = append(failures, fmt.Errorf("drain DCA data receiver: %w", err))
		}
	}
	if dcaRecording {
		stopContext, cancel := context.WithTimeout(context.Background(), prepared.commandTimeout)
		response, err := dcaControl.Stop(stopContext)
		cancel()
		if err == nil {
			err = requireStatus(response)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("stop DCA1000 during cleanup: %w", err))
		} else {
			log("DCA1000 stopped during cleanup")
			drainContext, drainCancel := context.WithTimeout(context.Background(), prepared.timings.controlDrain)
			statuses, drainErr := dcaControl.DrainStatuses(drainContext, prepared.timings.controlQuiet)
			drainCancel()
			if drainErr != nil {
				failures = append(failures, fmt.Errorf("drain DCA1000 control socket: %w", drainErr))
			}
			if statusErr := fatalAsyncError(statuses); statusErr != nil {
				failures = append(failures, statusErr)
			}
		}
	}
	if receiverCancel != nil {
		receiverCancel()
	}
	if receiver != nil {
		if err := receiver.Close(); err != nil {
			failures = append(failures, fmt.Errorf("close DCA data receiver: %w", err))
		}
	}
	if dcaUsed {
		if err := fatalAsyncError(dcaControl.TakeStatuses()); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func validateResult(plan radar.Plan, stats dca.CaptureStats, idle time.Duration, startIssuedAt time.Time) error {
	if stats.PacketsReceived == 0 {
		return errors.New("DCA1000 capture received no data packets")
	}
	if stats.MissingBytes != 0 || stats.DiscardedBeforeBasePackets != 0 {
		return fmt.Errorf(
			"DCA1000 capture is incomplete: missingBytes=%d discardedBeforeBase=%d",
			stats.MissingBytes,
			stats.DiscardedBeforeBasePackets,
		)
	}
	if stats.MalformedPackets != 0 {
		return fmt.Errorf("DCA1000 capture received %d malformed packet(s) from the configured device", stats.MalformedPackets)
	}
	if stats.OverlappingPackets != 0 {
		return fmt.Errorf("DCA1000 capture received %d overlapping packet(s)", stats.OverlappingPackets)
	}
	if stats.OutputBytes != plan.ExpectedBytes {
		return fmt.Errorf(
			"finite-frame capture size mismatch: expected=%d actual=%d",
			plan.ExpectedBytes,
			stats.OutputBytes,
		)
	}
	if err := validateFiniteFrameTiming(plan, stats, startIssuedAt); err != nil {
		return err
	}
	if maximum, err := plan.MaxDuration(idle); err != nil {
		return err
	} else if !stats.FirstPacketAt.IsZero() && !stats.LastPacketAt.IsZero() {
		actual := stats.LastPacketAt.Sub(stats.FirstPacketAt)
		if actual > maximum {
			return fmt.Errorf("finite-frame data exceeded planned maximum duration: span=%s maximum=%s", actual, maximum)
		}
	}
	return nil
}

func validateFiniteFrameTiming(
	plan radar.Plan,
	stats dca.CaptureStats,
	startIssuedAt time.Time,
) error {
	if startIssuedAt.IsZero() {
		return errors.New("finite-frame capture has no sensorStart issue timestamp for timing validation")
	}
	if stats.EarliestImpliedStartAt.IsZero() || stats.CadenceAnchorAt.IsZero() ||
		stats.CadenceAnchorEndOffset <= 0 {
		return errors.New("finite-frame capture has no packet cadence anchor for timing validation")
	}
	if plan.BytesPerFrame <= 0 || stats.CadenceAnchorEndOffset > stats.OutputBytes {
		return errors.New("finite-frame capture has inconsistent packet cadence metadata")
	}
	anchorFrame := uint64((stats.CadenceAnchorEndOffset - 1) / plan.BytesPerFrame)
	if anchorFrame != stats.CadenceAnchorFrame || anchorFrame >= uint64(plan.NumberOfFrames) {
		return errors.New("finite-frame capture cadence anchor falls outside the frame plan")
	}
	frameOffset, err := frameOffsetDuration(plan.FramePeriod, anchorFrame)
	if err != nil {
		return err
	}
	impliedStart := stats.CadenceAnchorAt.Add(-frameOffset)
	if !impliedStart.Equal(stats.EarliestImpliedStartAt) {
		return errors.New("finite-frame capture has inconsistent cadence anchor timestamps")
	}
	tolerance := time.Duration(0)
	if anchorFrame > 0 {
		tolerance, err = cadenceTolerance(plan.FramePeriod, frameOffset)
		if err != nil {
			return err
		}
	}
	if impliedStart.Add(tolerance).Before(startIssuedAt) {
		return fmt.Errorf(
			"finite-frame data arrived too early: frame=%d endOffset=%d earlyBy=%s tolerance=%s",
			anchorFrame,
			stats.CadenceAnchorEndOffset,
			startIssuedAt.Sub(impliedStart),
			tolerance,
		)
	}
	return nil
}

const cadenceClockDriftDivisor = int64(1000) // 1000 ppm (0.1%)

// MinimumFirstPacketTimeout derives a safe wait for DCA packet aggregation.
// A payload may not become available until the end of the last frame needed
// to fill it. A finite capture smaller than one payload also includes the raw
// tail guard that flushes its final partial packet.
func MinimumFirstPacketTimeout(plan radar.Plan) (time.Duration, error) {
	if plan.BytesPerFrame <= 0 || plan.FramePeriod <= 0 {
		return 0, errors.New("radar capture plan must have positive frame bytes and period")
	}
	var framesUntilPayload uint64
	includeTailGuard := false
	if plan.ExpectedBytes < int64(dca.MaximumDataPayloadSize) {
		if plan.NumberOfFrames == 0 {
			return 0, errors.New("finite radar capture plan has no frames")
		}
		framesUntilPayload = uint64(plan.NumberOfFrames)
		includeTailGuard = true
	} else {
		framesUntilPayload = (uint64(dca.MaximumDataPayloadSize) + uint64(plan.BytesPerFrame) - 1) /
			uint64(plan.BytesPerFrame)
	}
	duration, err := frameOffsetDuration(plan.FramePeriod, framesUntilPayload)
	if err != nil {
		return 0, fmt.Errorf("derive first DCA packet timeout: %w", err)
	}
	if includeTailGuard {
		if duration > time.Duration(math.MaxInt64)-dca.RawModeTailFlushGuard {
			return 0, errors.New("first DCA packet timeout exceeds supported duration")
		}
		duration += dca.RawModeTailFlushGuard
	}
	const schedulingGuard = time.Second
	if duration > time.Duration(math.MaxInt64)-schedulingGuard {
		return 0, errors.New("first DCA packet timeout exceeds supported duration")
	}
	return duration + schedulingGuard, nil
}

// MinimumReceiverIdleTimeout covers both a full packet assembled across small
// frames and the slower case where one fewer frame arrives before the FPGA
// flushes a partial packet. Finite receivers use the same bound after reaching
// their exact target so a slowly aggregated overlong stream cannot escape the
// post-target quiet verification.
func MinimumReceiverIdleTimeout(plan radar.Plan) (time.Duration, error) {
	if plan.BytesPerFrame <= 0 || plan.FramePeriod <= 0 {
		return 0, errors.New("radar capture plan must have positive frame bytes and period")
	}
	required := dca.RawModeTailFlushGuard
	framesPerPayload := (uint64(dca.MaximumDataPayloadSize) + uint64(plan.BytesPerFrame) - 1) /
		uint64(plan.BytesPerFrame)
	fullPacketGap, err := frameOffsetDuration(plan.FramePeriod, framesPerPayload)
	if err != nil {
		return 0, fmt.Errorf("derive DCA receiver idle timeout: %w", err)
	}
	longestPacketGap := fullPacketGap
	if framesPerPayload > 1 {
		partialPacketGap, err := frameOffsetDuration(plan.FramePeriod, framesPerPayload-1)
		if err != nil {
			return 0, fmt.Errorf("derive DCA partial-packet idle timeout: %w", err)
		}
		if partialPacketGap > time.Duration(math.MaxInt64)-dca.RawModeTailFlushGuard {
			return 0, errors.New("DCA receiver partial-packet idle timeout exceeds supported duration")
		}
		partialPacketGap += dca.RawModeTailFlushGuard
		if partialPacketGap > longestPacketGap {
			longestPacketGap = partialPacketGap
		}
	}
	guard := max(500*time.Millisecond, plan.FramePeriod/10)
	if longestPacketGap > time.Duration(math.MaxInt64)-guard {
		return 0, errors.New("DCA receiver idle timeout exceeds supported duration")
	}
	longestPacketGap += guard
	if longestPacketGap > required {
		required = longestPacketGap
	}
	return required, nil
}

func frameOffsetDuration(period time.Duration, frame uint64) (time.Duration, error) {
	if period <= 0 {
		return 0, errors.New("frame period must be positive")
	}
	if frame > uint64(math.MaxInt64/int64(period)) {
		return 0, errors.New("frame timing offset exceeds supported duration")
	}
	return time.Duration(frame * uint64(period)), nil
}

func cadenceTolerance(period, frameOffset time.Duration) (time.Duration, error) {
	base := min(max(period/10, time.Millisecond), 25*time.Millisecond)
	if half := period / 2; base > half {
		base = half
	}
	drift := frameOffset / time.Duration(cadenceClockDriftDivisor)
	if frameOffset%time.Duration(cadenceClockDriftDivisor) != 0 {
		drift++
	}
	if base > time.Duration(math.MaxInt64)-drift {
		return 0, errors.New("frame timing tolerance exceeds supported duration")
	}
	return base + drift, nil
}

func requireStatus(response dca.Response) error {
	if response.Status != 0 {
		return &dca.StatusError{Command: response.Command, Status: response.Status}
	}
	return nil
}

func fatalAsyncError(statuses []dca.Response) error {
	for _, status := range statuses {
		if dca.IsFatalSystemStatus(status.Status) {
			return fmt.Errorf("DCA1000 fatal async status: 0x%04X", status.Status)
		}
	}
	return nil
}

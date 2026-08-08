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
	"mmwcli/internal/capturestream"
	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

type Radar interface {
	VerifyPlatformContext(context.Context) (string, error)
	StopContext(context.Context) (string, error)
	ApplyContext(context.Context, radar.CapturePlan) error
	StartContext(context.Context) (string, error)
	StartWithoutReconfigurationContext(context.Context) (string, error)
}

// finiteFrameEndAwaiter is an optional capability for radar controllers whose
// finite frame schedule emits a trustworthy natural-completion event. Text CLI
// controllers intentionally fall back to the normal explicit stop path.
type finiteFrameEndAwaiter interface {
	AwaitFiniteFrameEndContext(context.Context) (string, error)
}

type DCAControl interface {
	Execute(context.Context, dca.Command, []byte) (dca.Response, error)
	Configure(context.Context, dca.FPGAConfig, int) (dca.ConfigurationResponses, error)
	StartRecordConvergent(context.Context) (dca.Response, error)
	StopRecord(context.Context) (dca.Response, error)
	TakeAsyncStatuses() []dca.Response
	DrainAsyncStatuses(context.Context, time.Duration) ([]dca.Response, error)
}

type DataReceiver interface {
	Start(context.Context, io.WriterAt) error
	WaitForFirst(context.Context) error
	Wait(context.Context) (dca.CaptureStats, error)
	Stats() dca.CaptureStats
	Close() error
}

type ReceiverFactory func(dca.ReceiverConfig) (DataReceiver, error)

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
}

// RadarFrameStartObserver is an optional Participant capability. The lower
// host timestamp precedes the radar start command; the upper timestamp is the
// first received ADC packet. Together they conservatively bracket frame start.
type RadarFrameStartObserver interface {
	RadarFrameStartObserved(lower, upper time.Time)
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

type Options struct {
	FPGAConfig          dca.FPGAConfig
	ReceiverConfig      dca.ReceiverConfig
	PacketDelay         int
	ResetFPGA           bool
	CommandTimeout      time.Duration
	DrainTimeout        time.Duration
	ControlDrainTimeout time.Duration
	ControlQuietWindow  time.Duration
	RadarCleanupTimeout time.Duration
	Mirror              *capturestream.Mirror
	MirrorSealTimeout   time.Duration
	Participant         Participant
	ParticipantTimeout  time.Duration
	Log                 func(string)
}

func DefaultOptions() Options {
	return Options{
		FPGAConfig:          dca.DefaultFPGAConfig(),
		ReceiverConfig:      dca.DefaultReceiverConfig(),
		PacketDelay:         25,
		CommandTimeout:      3 * time.Second,
		DrainTimeout:        3 * time.Second,
		ControlDrainTimeout: 500 * time.Millisecond,
		ControlQuietWindow:  50 * time.Millisecond,
		RadarCleanupTimeout: 3 * time.Second,
		MirrorSealTimeout:   3 * time.Second,
		ParticipantTimeout:  5 * time.Second,
	}
}

func Run(
	ctx context.Context,
	radarControl Radar,
	dcaControl DCAControl,
	newReceiver ReceiverFactory,
	plan radar.CapturePlan,
	output capturefile.Output,
	options Options,
) (stats dca.CaptureStats, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mirror := options.Mirror
	mirrorManagedByLifecycle := false
	defer func() {
		if mirror != nil && !mirrorManagedByLifecycle {
			mirror.Abort()
		}
	}()
	if radarControl == nil || dcaControl == nil || newReceiver == nil || !capturefile.IsUsableOutput(output) {
		return stats, errors.New("capture session dependencies are incomplete")
	}
	outputManagedByLifecycle := false
	defer func() {
		if outputManagedByLifecycle {
			return
		}
		if mirror != nil {
			mirror.Abort()
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
	if options.CommandTimeout <= 0 || options.DrainTimeout <= 0 ||
		options.ControlDrainTimeout <= 0 || options.ControlQuietWindow <= 0 ||
		options.RadarCleanupTimeout <= 0 {
		return stats, errors.New("capture command, data-drain, and control-drain timeouts must be positive")
	}
	if options.ReceiverConfig.FirstPacketTimeout <= 0 || options.ReceiverConfig.IdleTimeout <= 0 {
		return stats, errors.New("capture receiver first-packet and idle timeouts must be positive")
	}
	if mirror != nil {
		if !mirror.BoundTo(output) {
			return stats, errors.New("capture stream mirror is not bound to the session output")
		}
		if options.MirrorSealTimeout <= 0 {
			return stats, errors.New("capture stream mirror seal timeout must be positive")
		}
	}
	if options.Participant != nil && options.ParticipantTimeout <= 0 {
		return stats, errors.New("capture participant timeout must be positive")
	}
	if plan.Mode != radar.FullConfiguration && plan.Mode != radar.ReuseConfiguration {
		return stats, errors.New("radar capture plan has an invalid configuration mode")
	}
	if plan.BytesPerFrame <= 0 || plan.FramePeriod <= 0 {
		return stats, errors.New("radar capture plan must have positive frame bytes and period")
	}
	if plan.InfiniteFrames {
		if plan.NumberOfFrames != 0 || plan.ExpectedBytes != 0 {
			return stats, errors.New("infinite radar capture plan has inconsistent frame accounting")
		}
	} else if plan.NumberOfFrames == 0 || plan.ExpectedBytes <= 0 {
		return stats, errors.New("finite radar capture plan has inconsistent frame accounting")
	}
	if err := dca.ValidateRawCaptureFPGAConfig(options.FPGAConfig); err != nil {
		return stats, err
	}
	log := options.Log
	if log == nil {
		log = func(string) {}
	}
	if plan.ExpectedDCADataFormat != options.FPGAConfig.DataFormat {
		return stats, fmt.Errorf(
			"radar adcCfg requires DCA data-format=%d, configured=%d",
			plan.ExpectedDCADataFormat,
			options.FPGAConfig.DataFormat,
		)
	}
	if !plan.HardwareLVDSEnabled {
		return stats, errors.New("radar configuration does not enable hardware LVDS")
	}
	if plan.Mode == radar.ReuseConfiguration && plan.Dialect != radar.StudioCLI {
		return stats, errors.New("capture reuse without reconfiguration is only supported by TI studio_cli device firmware")
	}
	if plan.ExpectedBytes > 0 {
		if plan.BytesPerFrame <= 0 || plan.NumberOfFrames == 0 || plan.InfiniteFrames ||
			plan.ExpectedBytes%plan.BytesPerFrame != 0 ||
			plan.ExpectedBytes/plan.BytesPerFrame != int64(plan.NumberOfFrames) {
			return stats, errors.New("finite radar capture plan has inconsistent frame byte accounting")
		}
		if options.ReceiverConfig.MaxOutputBytes < plan.ExpectedBytes {
			return stats, fmt.Errorf(
				"DCA maximum output size %d is smaller than finite capture size %d",
				options.ReceiverConfig.MaxOutputBytes,
				plan.ExpectedBytes,
			)
		}
		options.ReceiverConfig.MaxOutputBytes = plan.ExpectedBytes
		options.ReceiverConfig.ExpectedOutputBytes = plan.ExpectedBytes
		options.ReceiverConfig.CadenceFrameBytes = plan.BytesPerFrame
		options.ReceiverConfig.CadenceFramePeriod = plan.FramePeriod
	} else {
		options.ReceiverConfig.ExpectedOutputBytes = 0
		options.ReceiverConfig.CadenceFrameBytes = 0
		options.ReceiverConfig.CadenceFramePeriod = 0
	}
	requiredIdleTimeout, err := MinimumReceiverIdleTimeout(plan)
	if err != nil {
		return stats, err
	}
	if options.ReceiverConfig.IdleTimeout < requiredIdleTimeout {
		log(fmt.Sprintf(
			"idle timeout raised from %s to %s for DCA raw tail/frame aggregation",
			options.ReceiverConfig.IdleTimeout,
			requiredIdleTimeout,
		))
		options.ReceiverConfig.IdleTimeout = requiredIdleTimeout
	}
	// After sensorStop, no new frames are expected; only the pending partial
	// payload needs its fixed tail-flush window.
	if options.DrainTimeout < dca.RawModeTailFlushGuard {
		options.DrainTimeout = dca.RawModeTailFlushGuard
	}
	requiredFirstPacketTimeout, err := MinimumFirstPacketTimeout(plan)
	if err != nil {
		return stats, err
	}
	if options.ReceiverConfig.FirstPacketTimeout < requiredFirstPacketTimeout {
		log(fmt.Sprintf(
			"first-packet timeout raised from %s to %s for DCA packet aggregation",
			options.ReceiverConfig.FirstPacketTimeout,
			requiredFirstPacketTimeout,
		))
		options.ReceiverConfig.FirstPacketTimeout = requiredFirstPacketTimeout
	}
	maximumStreamingDuration, finiteDuration, err := plan.MaximumStreamingDuration(options.ReceiverConfig.IdleTimeout)
	if err != nil {
		return stats, err
	}
	if plan.ExpectedBytes > 0 && !finiteDuration {
		return stats, errors.New("finite radar capture plan has no bounded streaming duration")
	}

	var receiver DataReceiver
	var receiverCancel context.CancelFunc
	receiverStarted := false
	dcaUsed := false
	dcaRecording := false
	radarMayBeRunning := false
	finiteFrameCompleted := false
	var radarStartIssuedAt time.Time
	participant := options.Participant
	participantActive := false

	outputManagedByLifecycle = true
	mirrorManagedByLifecycle = true
	defer func() {
		cleanupErr := cleanup(
			radarControl,
			dcaControl,
			receiver,
			receiverCancel,
			receiverStarted,
			radarMayBeRunning,
			finiteFrameCompleted && ctx.Err() == nil,
			dcaUsed,
			dcaRecording,
			options,
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
		if cleanupErr != nil {
			marked := &CleanupError{Err: cleanupErr}
			if resultErr == nil {
				resultErr = marked
			} else {
				resultErr = errors.Join(resultErr, marked)
			}
		}
		if cancellationErr := ctx.Err(); cancellationErr != nil {
			resultErr = errors.Join(resultErr, cancellationErr)
		}
		if resultErr == nil {
			resultErr = validateResult(plan, stats, options.ReceiverConfig.IdleTimeout, radarStartIssuedAt)
		}
		if participantActive {
			participantContext, cancelParticipant := context.WithTimeout(
				context.Background(),
				options.ParticipantTimeout,
			)
			participantErr := participant.Finish(participantContext, resultErr == nil)
			cancelParticipant()
			if participantErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("finish capture participant: %w", participantErr))
			}
		}
		if mirror != nil {
			if mirrorErr := mirror.Err(); mirrorErr != nil {
				resultErr = errors.Join(resultErr, mirrorErr)
			}
			if resultErr != nil {
				mirror.Abort()
			} else {
				sealContext, cancelSeal := context.WithTimeout(
					context.Background(),
					options.MirrorSealTimeout,
				)
				resultErr = mirror.Seal(sealContext)
				cancelSeal()
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
			if mirror != nil {
				mirror.Abort()
			}
			if closeErr := output.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, &CleanupError{Err: closeErr})
			}
		}
	}()

	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if _, err := radarControl.VerifyPlatformContext(ctx); err != nil {
		return stats, err
	}
	if _, err := radarControl.StopContext(ctx); err != nil {
		return stats, fmt.Errorf("establish stopped radar state: %w", err)
	}
	log("radar stopped")

	dcaUsed = true
	response, err := dcaControl.StopRecord(ctx)
	if err != nil {
		return stats, fmt.Errorf("establish stopped DCA state: %w", err)
	}
	if err := requireStatus(response); err != nil {
		return stats, err
	}
	log("DCA1000 stopped")

	if options.ResetFPGA {
		response, err = dcaControl.Execute(ctx, dca.CommandResetFPGA, nil)
		if err != nil {
			return stats, err
		}
		if err := requireStatus(response); err != nil {
			return stats, err
		}
		log("DCA1000 FPGA reset")
	}
	if _, err := dcaControl.Configure(ctx, options.FPGAConfig, options.PacketDelay); err != nil {
		return stats, err
	}
	if err := fatalAsyncError(dcaControl.TakeAsyncStatuses()); err != nil {
		return stats, err
	}
	log("DCA1000 configured")

	if plan.Mode == radar.FullConfiguration {
		if err := radarControl.ApplyContext(ctx, plan); err != nil {
			return stats, fmt.Errorf("apply radar configuration: %w", err)
		}
		log("radar configuration applied")
	} else {
		log("radar configuration reuse selected; no RF/LVDS command sent")
	}

	if participant != nil {
		participantActive = true
		participantContext, cancelParticipant := context.WithTimeout(ctx, options.ParticipantTimeout)
		err = participant.Arm(participantContext)
		cancelParticipant()
		if err != nil {
			return stats, fmt.Errorf("arm capture participant: %w", err)
		}
		log("capture participant armed")
	}

	receiver, err = newReceiver(options.ReceiverConfig)
	if err != nil {
		return stats, err
	}
	receiverContext, cancelReceiver := context.WithCancel(context.Background())
	receiverCancel = cancelReceiver
	receiverOutput := io.WriterAt(output)
	if mirror != nil {
		receiverOutput = mirror
	}
	if err := receiver.Start(receiverContext, receiverOutput); err != nil {
		return stats, err
	}
	receiverStarted = true
	log("DCA1000 data socket armed")

	response, err = dcaControl.StartRecordConvergent(ctx)
	if err != nil {
		// StartRecordConvergent has already sent the one permitted StopRecord;
		// dcaRecording remains false so cleanup will not send another.
		return stats, err
	}
	if err := requireStatus(response); err != nil {
		return stats, err
	}
	dcaRecording = true
	if err := fatalAsyncError(dcaControl.TakeAsyncStatuses()); err != nil {
		return stats, err
	}
	log("DCA1000 recording started")

	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if participant != nil {
		participantContext, cancelParticipant := context.WithTimeout(ctx, options.ParticipantTimeout)
		err = participant.Start(participantContext)
		cancelParticipant()
		if err != nil {
			return stats, fmt.Errorf("start capture participant: %w", err)
		}
		log("capture participant started")
	}
	radarMayBeRunning = true
	radarStartIssuedAt = time.Now()
	if plan.Mode == radar.ReuseConfiguration {
		_, err = radarControl.StartWithoutReconfigurationContext(ctx)
	} else {
		_, err = radarControl.StartContext(ctx)
	}
	if err != nil {
		return stats, fmt.Errorf("start radar: %w", err)
	}
	log("radar started")

	if err := receiver.WaitForFirst(ctx); err != nil {
		return stats, err
	}
	firstPacketAt := receiver.Stats().FirstPacketAt
	if observer, ok := participant.(RadarFrameStartObserver); ok && !firstPacketAt.IsZero() {
		observer.RadarFrameStartObserved(radarStartIssuedAt, firstPacketAt)
	}
	if plan.InfiniteFrames {
		stats, err = receiver.Wait(ctx)
		if err == nil && ctx.Err() == nil {
			return stats, errors.New("infinite-frame capture became idle unexpectedly")
		}
		return stats, err
	}

	if firstPacketAt.IsZero() {
		return stats, errors.New("DCA1000 receiver reported a first packet without a timestamp")
	}
	waitContext, cancelWait := context.WithDeadline(ctx, firstPacketAt.Add(maximumStreamingDuration))
	stats, err = receiver.Wait(waitContext)
	cancelWait()
	if err == nil && ctx.Err() == nil &&
		validateResult(plan, stats, options.ReceiverConfig.IdleTimeout, radarStartIssuedAt) == nil {
		finiteFrameCompleted = true
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return stats, &finiteCaptureDeadlineError{maximum: maximumStreamingDuration}
	}
	return stats, err
}

func cleanup(
	radarControl Radar,
	dcaControl DCAControl,
	receiver DataReceiver,
	receiverCancel context.CancelFunc,
	receiverStarted bool,
	radarMayBeRunning bool,
	finiteFrameCompleted bool,
	dcaUsed bool,
	dcaRecording bool,
	options Options,
	log func(string),
) error {
	var failures []error
	if radarMayBeRunning {
		stopContext, cancel := context.WithTimeout(context.Background(), options.RadarCleanupTimeout)
		operation := "stop radar"
		var err error
		if awaiter, ok := radarControl.(finiteFrameEndAwaiter); ok && finiteFrameCompleted {
			operation = "wait for finite radar frame end"
			_, err = awaiter.AwaitFiniteFrameEndContext(stopContext)
		} else {
			_, err = radarControl.StopContext(stopContext)
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
		drainContext, cancel := context.WithTimeout(context.Background(), options.DrainTimeout)
		_, err := receiver.Wait(drainContext)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, dca.ErrFirstPacketTimeout) && !errors.Is(err, dca.ErrReceiverClosed) {
			failures = append(failures, fmt.Errorf("drain DCA data receiver: %w", err))
		}
	}
	if dcaRecording {
		stopContext, cancel := context.WithTimeout(context.Background(), options.CommandTimeout)
		response, err := dcaControl.StopRecord(stopContext)
		cancel()
		if err == nil {
			err = requireStatus(response)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("stop DCA1000 during cleanup: %w", err))
		} else {
			log("DCA1000 stopped during cleanup")
			drainContext, drainCancel := context.WithTimeout(context.Background(), options.ControlDrainTimeout)
			statuses, drainErr := dcaControl.DrainAsyncStatuses(drainContext, options.ControlQuietWindow)
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
		if err := fatalAsyncError(dcaControl.TakeAsyncStatuses()); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func validateResult(plan radar.CapturePlan, stats dca.CaptureStats, idle time.Duration, startIssuedAt time.Time) error {
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
	if plan.ExpectedBytes > 0 && stats.OutputBytes != plan.ExpectedBytes {
		return fmt.Errorf(
			"finite-frame capture size mismatch: expected=%d actual=%d",
			plan.ExpectedBytes,
			stats.OutputBytes,
		)
	}
	if !plan.InfiniteFrames {
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
	}
	if maximum, finite, err := plan.MaximumStreamingDuration(idle); err != nil {
		return err
	} else if finite && !stats.FirstPacketAt.IsZero() && !stats.LastPacketAt.IsZero() {
		actual := stats.LastPacketAt.Sub(stats.FirstPacketAt)
		if actual > maximum {
			return fmt.Errorf("finite-frame data exceeded planned maximum duration: span=%s maximum=%s", actual, maximum)
		}
	}
	return nil
}

const cadenceClockDriftDivisor = int64(1000) // 1000 ppm (0.1%)

// MinimumFirstPacketTimeout derives a safe wait for DCA packet aggregation.
// A payload may not become available until the end of the last frame needed
// to fill it. A finite capture smaller than one payload also includes the raw
// tail guard that flushes its final partial packet.
func MinimumFirstPacketTimeout(plan radar.CapturePlan) (time.Duration, error) {
	if plan.BytesPerFrame <= 0 || plan.FramePeriod <= 0 {
		return 0, errors.New("radar capture plan must have positive frame bytes and period")
	}
	var framesUntilPayload uint64
	includeTailGuard := false
	if !plan.InfiniteFrames && plan.ExpectedBytes > 0 && plan.ExpectedBytes < int64(dca.MaximumDataPayloadSize) {
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
func MinimumReceiverIdleTimeout(plan radar.CapturePlan) (time.Duration, error) {
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

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturestream"
	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
	"mmwcli/internal/multisensorstream"
	"mmwcli/internal/session"
)

const (
	aggregateRadarSourceID      = "radar-0"
	aggregateParticipantTimeout = 5 * time.Second
)

type aggregateCoordinator interface {
	Arm(context.Context) error
	Start(context.Context) error
	Finish(context.Context, bool) error
	Result() (multisensorcapture.Result, error)
}

type activeMultisensorCapture struct {
	directory      *multisensor.Directory
	radarDirectory *capturefile.SessionDirectory
	coordinator    aggregateCoordinator
	sessionID      string
	prepared       preparedCaptureOutput
	hostOrigin     time.Time
	cancelCapture  context.CancelFunc
	stream         *activeMultisensorStream

	mu                  sync.Mutex
	frameStart          multisensor.FrameStartBracket
	frameStartObserved  bool
	frameStartErr       error
	requiredFailure     error
	participantFinished bool
	participantComplete bool
}

type activeMultisensorStream struct {
	adapter *multisensorstream.StdoutAdapter
	mirror  *capturestream.Mirror
}

func createCaptureDestination(
	ctx context.Context,
	outputPath string,
	multisensorPlanPath string,
	prepared preparedCaptureOutput,
	producerStderr io.Writer,
	streamStdout *os.File,
	cancelCapture context.CancelFunc,
) (*capturefile.SessionDirectory, *activeMultisensorCapture, error) {
	if multisensorPlanPath == "" {
		output, err := createCaptureOutput(outputPath, prepared.finalizeSession)
		return output, nil, err
	}
	plan, err := multisensorcapture.LoadPlan(multisensorPlanPath)
	if err != nil {
		return nil, nil, err
	}
	for _, source := range plan.Sources {
		if source.SourceID == aggregateRadarSourceID {
			return nil, nil, usageError{message: `--multisensor-plan reserves source_id "radar-0" for radar`}
		}
	}
	sessionID, err := multisensor.NewSessionID()
	if err != nil {
		return nil, nil, err
	}
	directory, err := multisensor.CreateDirectory(outputPath)
	if err != nil {
		return nil, nil, err
	}
	radarPath, err := directory.SourcePath(aggregateRadarSourceID)
	if err != nil {
		return nil, nil, err
	}
	radarDirectory, err := createCaptureOutput(radarPath, prepared.finalizeSession)
	if err != nil {
		return nil, nil, err
	}
	capture := &activeMultisensorCapture{
		directory: directory, radarDirectory: radarDirectory, sessionID: sessionID,
		prepared: prepared, hostOrigin: time.Now(), cancelCapture: cancelCapture,
	}
	if streamStdout != nil {
		capture.stream, err = startMultisensorStream(
			ctx, streamStdout, radarDirectory, prepared, plan, sessionID, cancelCapture,
		)
		if err != nil {
			_ = radarDirectory.Close()
			return nil, nil, err
		}
	}
	var itemSink multisensorcapture.ItemSink
	if capture.stream != nil {
		itemSink = capture.stream.adapter
	}
	coordinator, err := multisensorcapture.Start(ctx, plan, sessionID, directory, multisensorcapture.Options{
		Stderr: producerStderr, ItemSink: itemSink,
		OnRequiredFailure: func(err error) {
			capture.recordRequiredFailure(err)
		},
	})
	if err != nil {
		_ = radarDirectory.Close()
		return nil, nil, capture.abortStream(err, multisensorstream.AbortSourceFailed)
	}
	capture.coordinator = coordinator
	return radarDirectory, capture, nil
}

func startMultisensorStream(
	ctx context.Context,
	stdout *os.File,
	radarDirectory *capturefile.SessionDirectory,
	prepared preparedCaptureOutput,
	plan multisensorcapture.Plan,
	sessionID string,
	cancelCapture context.CancelFunc,
) (*activeMultisensorStream, error) {
	streamSession := multisensorstream.Session{
		SessionID: sessionID, SynchronizationGrade: multisensorstream.SynchronizationSoftwareBarrier,
		Sources: []multisensorstream.Source{{
			SourceID: aggregateRadarSourceID, Kind: multisensorstream.SourceRadar, Required: true,
			Payload: multisensorstream.PayloadContract{
				Filename: capturefile.SessionADCFileName,
				Format:   prepared.plan.RawCapture.ConfigFormat(),
			},
			Clock: multisensorstream.Clock{
				ClockID: aggregateRadarSourceID + "-frame-clock", TickHz: uint64(time.Second),
				TimestampSemantics: multisensorstream.TimestampFrameStart,
			},
			Limits: multisensorstream.SourceLimits{
				MaxItems: uint64(prepared.plan.NumberOfFrames), MaxItemBytes: uint64(prepared.plan.BytesPerFrame),
				MaxPayloadBytes: uint64(prepared.plan.ExpectedBytes),
			},
		}},
	}
	for _, source := range plan.Sources {
		streamSession.Sources = append(streamSession.Sources, multisensorstream.Source{
			SourceID: source.SourceID, Kind: source.Kind, Required: source.Required,
			Payload: source.Payload, Clock: source.Clock, Limits: source.Limits,
		})
	}
	adapter, err := multisensorstream.NewStdoutAdapter(ctx, stdout, streamSession)
	if err != nil {
		return nil, err
	}
	abort := func(cause error) error {
		terminalCtx, cancel := context.WithTimeout(context.Background(), captureStreamTerminalTimeout)
		defer cancel()
		return errors.Join(cause, adapter.Abort(terminalCtx, multisensorstream.AbortIntegrityFailed), adapter.Close())
	}
	if err := adapter.WriteRadarConfig(
		ctx, aggregateRadarSourceID, prepared.plan.RawCapture.ConfigFormat(), prepared.configSnapshot,
	); err != nil {
		return nil, errors.Join(err, adapter.Close())
	}
	radarSink, err := multisensorstream.NewRadarSink(adapter, aggregateRadarSourceID, prepared.plan.FramePeriod)
	if err != nil {
		return nil, abort(err)
	}
	mirror, err := capturestream.NewMirror(
		radarDirectory,
		radarSink,
		cancelCapture,
		capturestream.DefaultMirrorConfig(
			prepared.plan.BytesPerFrame,
			uint64(prepared.plan.NumberOfFrames),
		),
	)
	if err != nil {
		return nil, abort(err)
	}
	return &activeMultisensorStream{adapter: adapter, mirror: mirror}, nil
}

func (capture *activeMultisensorCapture) Arm(ctx context.Context) error {
	return capture.coordinator.Arm(ctx)
}

func (capture *activeMultisensorCapture) Start(ctx context.Context) error {
	return capture.coordinator.Start(ctx)
}

func (capture *activeMultisensorCapture) Finish(ctx context.Context, complete bool) error {
	err := capture.coordinator.Finish(ctx, complete)
	capture.mu.Lock()
	capture.participantFinished = true
	if err == nil && complete {
		err = capture.frameStartErr
		if err == nil && !capture.frameStartObserved {
			err = errors.New("multisensor radar frame-start observation is missing")
		}
	}
	capture.participantComplete = complete && err == nil
	capture.mu.Unlock()
	return err
}

func (capture *activeMultisensorCapture) RadarFrameStartObserved(lower, upper time.Time) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.frameStartObserved || capture.frameStartErr != nil {
		capture.frameStartErr = errors.New("multisensor radar frame-start observation was repeated")
		return
	}
	if lower.Before(capture.hostOrigin) || upper.Before(lower) {
		capture.frameStartErr = errors.New("multisensor radar frame-start observation is outside the session clock")
		return
	}
	capture.frameStart = multisensor.FrameStartBracket{
		LowerNS: uint64(lower.Sub(capture.hostOrigin)),
		UpperNS: uint64(upper.Sub(capture.hostOrigin)),
	}
	capture.frameStartObserved = true
}

func (capture *activeMultisensorCapture) recordRequiredFailure(err error) {
	if err == nil {
		return
	}
	capture.mu.Lock()
	if capture.requiredFailure == nil {
		capture.requiredFailure = err
	}
	capture.mu.Unlock()
	if capture.cancelCapture != nil {
		capture.cancelCapture()
	}
}

func (capture *activeMultisensorCapture) finish(ctx context.Context, resultErr error) error {
	if capture == nil {
		return resultErr
	}
	capture.mu.Lock()
	requiredErr := capture.requiredFailure
	participantFinished := capture.participantFinished
	participantComplete := capture.participantComplete
	frameStart := capture.frameStart
	frameStartErr := capture.frameStartErr
	observed := capture.frameStartObserved
	capture.mu.Unlock()
	resultErr = errors.Join(resultErr, requiredErr)
	if resultErr != nil {
		if !participantFinished && capture.coordinator != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), aggregateParticipantTimeout)
			resultErr = errors.Join(resultErr, capture.coordinator.Finish(cleanupCtx, false))
			cancel()
		}
		reason := multisensorStreamAbortReason(resultErr)
		if requiredErr != nil {
			reason = multisensorstream.AbortSourceFailed
		}
		return capture.abortStream(resultErr, reason)
	}
	if !participantFinished || !participantComplete {
		return capture.abortStream(
			errors.New("multisensor participant did not complete"),
			multisensorstream.AbortIntegrityFailed,
		)
	}
	if frameStartErr != nil || !observed {
		return capture.abortStream(
			errors.Join(errors.New("multisensor radar frame-start evidence is unavailable"), frameStartErr),
			multisensorstream.AbortIntegrityFailed,
		)
	}
	result, err := capture.coordinator.Result()
	if err != nil {
		return capture.abortStream(err, multisensorstream.AbortSourceFailed)
	}
	wantHostClock := multisensor.Clock{
		ClockID: multisensor.HostClockID, TickHz: 1_000_000_000,
		TimestampSemantics: multisensor.TimestampHostMonotonic,
	}
	if result.PlanSchema != multisensorcapture.PlanSchema || result.SessionID != capture.sessionID ||
		result.SynchronizationGrade != multisensor.SynchronizationSoftwareBarrier || result.HostClock != wantHostClock {
		return capture.abortStream(
			errors.New("multisensor coordinator result does not match the aggregate session"),
			multisensorstream.AbortIntegrityFailed,
		)
	}
	radarSource, err := multisensor.FinalizeRadarSource(
		ctx,
		capture.radarDirectory.FinalPath(),
		capture.prepared.configSnapshot,
		capture.prepared.plan,
		Version,
		frameStart,
		aggregateRadarSourceID,
	)
	if err != nil {
		return capture.abortStream(err, multisensorstream.AbortIntegrityFailed)
	}
	sources := make([]multisensor.Source, 0, len(result.Sources)+1)
	sources = append(sources, radarSource)
	sources = append(sources, result.Sources...)
	sessionRecord, err := multisensor.NewSoftwareBarrierSession(
		capture.sessionID,
		sources,
		result.ApplicationMetadata,
	)
	if err != nil {
		return capture.abortStream(err, multisensorstream.AbortIntegrityFailed)
	}
	if err := capture.directory.CommitContext(ctx, sessionRecord); err != nil {
		return capture.abortStream(
			fmt.Errorf("publish multisensor capture: %w", err),
			multisensorstream.AbortPublishFailed,
		)
	}
	if capture.stream != nil {
		terminalCtx, cancel := context.WithTimeout(context.Background(), captureStreamTerminalTimeout)
		defer cancel()
		if err := capture.stream.adapter.EndSource(
			terminalCtx, aggregateRadarSourceID, multisensorstream.OutcomeComplete,
		); err != nil {
			return errors.Join(err, capture.stream.adapter.Close())
		}
		for _, source := range result.Sources {
			if err := capture.stream.adapter.EndSource(terminalCtx, source.SourceID, source.Outcome); err != nil {
				return errors.Join(err, capture.stream.adapter.Close())
			}
		}
		artifact, err := capture.directory.CommittedSessionArtifact()
		if err != nil {
			return errors.Join(err, capture.stream.adapter.Close())
		}
		err = capture.stream.adapter.Commit(terminalCtx, multisensorstream.SessionArtifact{
			SizeBytes: artifact.SizeBytes, SHA256: artifact.SHA256,
		})
		return errors.Join(err, capture.stream.adapter.Close())
	}
	return nil
}

func (capture *activeMultisensorCapture) abortStream(
	cause error,
	reason multisensorstream.AbortReason,
) error {
	if capture == nil || capture.stream == nil {
		return cause
	}
	capture.stream.mirror.Abort()
	terminalCtx, cancel := context.WithTimeout(context.Background(), captureStreamTerminalTimeout)
	defer cancel()
	return errors.Join(cause, capture.stream.adapter.Abort(terminalCtx, reason), capture.stream.adapter.Close())
}

func multisensorStreamAbortReason(err error) multisensorstream.AbortReason {
	if errors.Is(err, capturestream.ErrMirrorBackpressure) {
		return multisensorstream.AbortBackpressure
	}
	if errors.Is(err, capturestream.ErrMirrorIntegrity) {
		return multisensorstream.AbortIntegrityFailed
	}
	var cleanup *session.CleanupError
	if errors.As(err, &cleanup) {
		return multisensorstream.AbortCleanupFailed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return multisensorstream.AbortCancelled
	}
	return multisensorstream.AbortIntegrityFailed
}

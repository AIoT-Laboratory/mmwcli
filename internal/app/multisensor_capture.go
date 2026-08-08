package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
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

	mu                  sync.Mutex
	frameStart          multisensor.FrameStartBracket
	frameStartObserved  bool
	frameStartErr       error
	requiredFailure     error
	participantFinished bool
	participantComplete bool
}

func createCaptureDestination(
	ctx context.Context,
	outputPath string,
	multisensorPlanPath string,
	prepared preparedCaptureOutput,
	producerStderr io.Writer,
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
	coordinator, err := multisensorcapture.Start(ctx, plan, sessionID, directory, multisensorcapture.Options{
		Stderr: producerStderr,
		OnRequiredFailure: func(err error) {
			capture.recordRequiredFailure(err)
		},
	})
	if err != nil {
		_ = radarDirectory.Close()
		return nil, nil, err
	}
	capture.coordinator = coordinator
	return radarDirectory, capture, nil
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
		return resultErr
	}
	if !participantFinished || !participantComplete {
		return errors.New("multisensor participant did not complete")
	}
	if frameStartErr != nil || !observed {
		return errors.Join(errors.New("multisensor radar frame-start evidence is unavailable"), frameStartErr)
	}
	result, err := capture.coordinator.Result()
	if err != nil {
		return err
	}
	wantHostClock := multisensor.Clock{
		ClockID: multisensor.HostClockID, TickHz: 1_000_000_000,
		TimestampSemantics: multisensor.TimestampHostMonotonic,
	}
	if result.PlanSchema != multisensorcapture.PlanSchema || result.SessionID != capture.sessionID ||
		result.SynchronizationGrade != multisensor.SynchronizationSoftwareBarrier || result.HostClock != wantHostClock {
		return errors.New("multisensor coordinator result does not match the aggregate session")
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
		return err
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
		return err
	}
	if err := capture.directory.CommitContext(ctx, sessionRecord); err != nil {
		return fmt.Errorf("publish multisensor capture: %w", err)
	}
	return nil
}

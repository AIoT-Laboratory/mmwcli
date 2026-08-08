package multisensorcapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/sensorproducer"
)

const DefaultReadyTimeout = 5 * time.Second

var sessionIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// ProducerProcess is the bounded producer surface owned by Coordinator.
// Production callers use sensorproducer.Process through the default starter;
// tests can provide an in-process producer without owning hardware or a child
// process.
type ProducerProcess interface {
	Ready(context.Context) error
	Arm(context.Context) error
	Start(context.Context) error
	Stop(context.Context) error
	Cancel(context.Context) error
	Next(context.Context) (sensorproducer.Record, error)
	Wait(context.Context) error
	Kill() error
}

type StartProducerFunc func(
	context.Context,
	[]string,
	string,
	string,
	sensorproducer.ProcessOptions,
) (ProducerProcess, error)

type Options struct {
	ReadyTimeout      time.Duration
	Stderr            io.Writer
	StartProducer     StartProducerFunc
	OnRequiredFailure func(error)
}

// Result contains only external sources. The app adds the one authoritative
// radar source before publishing mmwcli.multisensor_session.v1.
type Result struct {
	PlanSchema           string
	SessionID            string
	SynchronizationGrade multisensor.SynchronizationGrade
	HostClock            multisensor.Clock
	ApplicationMetadata  multisensor.ApplicationMetadata
	Sources              []multisensor.Source
}

type coordinatorPhase uint8

const (
	phaseReady coordinatorPhase = iota + 1
	phaseArmed
	phaseStarted
	phaseFinished
	phaseFailed
)

type sourceSlot struct {
	plan    SourcePlan
	worker  *sourceWorker
	failure error
}

type Coordinator struct {
	mu sync.Mutex

	plan      Plan
	sessionID string
	directory *multisensor.Directory
	slots     []sourceSlot
	phase     coordinatorPhase
	result    *Result

	onRequiredFailure func(error)
	failureOnce       sync.Once
}

// Start launches each external producer and completes its READY handshake.
// A caller with required sources must supply OnRequiredFailure; it normally
// cancels the enclosing radar capture context when a producer fails while
// streaming.
func Start(
	ctx context.Context,
	plan Plan,
	sessionID string,
	directory *multisensor.Directory,
	options Options,
) (*Coordinator, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if !sessionIDPattern.MatchString(sessionID) {
		return nil, errors.New("multisensor capture session_id must be a lowercase UUIDv4")
	}
	if directory == nil || directory.PartPath() == "" || directory.Committed() {
		return nil, errors.New("multisensor capture directory is not open")
	}
	required := false
	for _, source := range plan.Sources {
		required = required || source.Required
	}
	if required && options.OnRequiredFailure == nil {
		return nil, errors.New("OnRequiredFailure is required when the plan has required external sources")
	}
	readyTimeout := options.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = DefaultReadyTimeout
	}
	if readyTimeout < 0 {
		return nil, errors.New("ReadyTimeout must be positive or zero for the default")
	}
	starter := options.StartProducer
	if starter == nil {
		starter = startProcessProducer
	}

	coordinator := &Coordinator{
		plan: clonePlan(plan), sessionID: sessionID, directory: directory,
		phase: phaseReady, onRequiredFailure: options.OnRequiredFailure,
		slots: make([]sourceSlot, 0, len(plan.Sources)),
	}
	for _, sourcePlan := range plan.Sources {
		slot := sourceSlot{plan: sourcePlan}
		var process ProducerProcess
		sourcePath, err := directory.CreateSourceDirectory(sourcePlan.SourceID)
		if err == nil {
			process, err = starter(ctx, append([]string(nil), sourcePlan.Argv...), sessionID, sourcePlan.SourceID,
				sensorproducer.ProcessOptions{Stderr: options.Stderr, QueueSize: sourcePlan.QueueSize})
			if err == nil {
				slot.worker, err = newSourceWorker(ctx, sessionID, sourcePlan, sourcePath, process,
					func(failure error) { coordinator.requiredFailure(sourcePlan, failure) })
			}
		}
		if err == nil {
			readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
			err = slot.worker.process.Ready(readyCtx)
			cancel()
		}
		if err != nil {
			slot.failure = fmt.Errorf("source %q READY: %w", sourcePlan.SourceID, err)
			if slot.worker != nil {
				_ = slot.worker.abort(context.Background())
			} else if process != nil {
				_ = process.Kill()
			}
			if removeErr := removeSourceDirectory(directory, sourcePlan.SourceID); removeErr != nil {
				slot.failure = errors.Join(slot.failure, removeErr)
			}
			coordinator.slots = append(coordinator.slots, slot)
			if sourcePlan.Required {
				coordinator.phase = phaseFailed
				_ = coordinator.abortAll(context.Background())
				return nil, slot.failure
			}
			continue
		}
		coordinator.slots = append(coordinator.slots, slot)
	}
	return coordinator, nil
}

func (coordinator *Coordinator) Arm(ctx context.Context) error {
	return coordinator.transition(ctx, phaseReady, phaseArmed, "ARM", func(worker *sourceWorker) error {
		return worker.process.Arm(ctx)
	})
}

func (coordinator *Coordinator) Start(ctx context.Context) error {
	return coordinator.transition(ctx, phaseArmed, phaseStarted, "START", func(worker *sourceWorker) error {
		return worker.process.Start(ctx)
	})
}

func (coordinator *Coordinator) transition(
	ctx context.Context,
	want coordinatorPhase,
	next coordinatorPhase,
	operation string,
	command func(*sourceWorker) error,
) error {
	if coordinator == nil {
		return errors.New("multisensor coordinator is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.phase != want {
		return fmt.Errorf("multisensor coordinator %s is invalid in phase %d", operation, coordinator.phase)
	}
	for index := range coordinator.slots {
		slot := &coordinator.slots[index]
		if slot.failure != nil {
			continue
		}
		if err := command(slot.worker); err != nil {
			slot.failure = fmt.Errorf("source %q %s: %w", slot.plan.SourceID, operation, err)
			_ = slot.worker.abort(ctx)
			if removeErr := removeSourceDirectory(coordinator.directory, slot.plan.SourceID); removeErr != nil {
				slot.failure = errors.Join(slot.failure, removeErr)
			}
			if slot.plan.Required {
				coordinator.phase = phaseFailed
				_ = coordinator.abortAllLocked(ctx)
				return slot.failure
			}
		}
	}
	coordinator.phase = next
	return nil
}

// Finish implements session.Participant. A successful finish stops producers,
// drains END/EOF, validates their evidence, and freezes Result. An abort sends
// CANCEL and leaves no publishable result.
func (coordinator *Coordinator) Finish(ctx context.Context, complete bool) error {
	if coordinator == nil {
		return errors.New("multisensor coordinator is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !complete {
		if coordinator.phase == phaseFinished {
			return errors.New("multisensor coordinator is already finished")
		}
		err := coordinator.abortAllLocked(ctx)
		coordinator.phase = phaseFailed
		return err
	}
	if coordinator.phase != phaseStarted {
		return fmt.Errorf("multisensor coordinator Finish(true) is invalid in phase %d", coordinator.phase)
	}

	var requiredErr error
	for index := range coordinator.slots {
		slot := &coordinator.slots[index]
		if slot.failure != nil {
			continue
		}
		if err := slot.worker.process.Stop(ctx); err != nil {
			slot.failure = fmt.Errorf("source %q STOP: %w", slot.plan.SourceID, err)
			_ = slot.worker.abort(ctx)
			if slot.plan.Required {
				requiredErr = errors.Join(requiredErr, slot.failure)
			}
		}
	}
	for index := range coordinator.slots {
		slot := &coordinator.slots[index]
		if slot.failure == nil {
			source, err := slot.worker.collect(ctx)
			if err == nil {
				slot.worker.source = source
			} else {
				slot.failure = fmt.Errorf("source %q stream: %w", slot.plan.SourceID, err)
				_ = slot.worker.abort(ctx)
				if slot.plan.Required {
					requiredErr = errors.Join(requiredErr, slot.failure)
				}
			}
		}
		if slot.failure != nil {
			if removeErr := removeSourceDirectory(coordinator.directory, slot.plan.SourceID); removeErr != nil {
				slot.failure = errors.Join(slot.failure, removeErr)
				if slot.plan.Required {
					requiredErr = errors.Join(requiredErr, removeErr)
				}
			}
		}
	}
	if requiredErr != nil {
		_ = coordinator.abortAllLocked(ctx)
		coordinator.phase = phaseFailed
		return requiredErr
	}

	sources := make([]multisensor.Source, 0, len(coordinator.slots))
	for index := range coordinator.slots {
		slot := &coordinator.slots[index]
		if slot.failure == nil {
			sources = append(sources, cloneSource(slot.worker.source))
			continue
		}
		source, err := failedSource(slot.plan, slot.worker)
		if err != nil {
			coordinator.phase = phaseFailed
			return fmt.Errorf("source %q failed contract: %w", slot.plan.SourceID, err)
		}
		sources = append(sources, source)
	}
	coordinator.result = &Result{
		PlanSchema: PlanSchema, SessionID: coordinator.sessionID,
		SynchronizationGrade: multisensor.SynchronizationSoftwareBarrier,
		HostClock: multisensor.Clock{
			ClockID: "host-monotonic", TickHz: 1_000_000_000,
			TimestampSemantics: multisensor.TimestampHostMonotonic,
		},
		ApplicationMetadata: cloneMetadata(coordinator.plan.ApplicationMetadata),
		Sources:             sources,
	}
	coordinator.phase = phaseFinished
	return nil
}

func (coordinator *Coordinator) Sources() ([]multisensor.Source, error) {
	result, err := coordinator.Result()
	if err != nil {
		return nil, err
	}
	return result.Sources, nil
}

func (coordinator *Coordinator) Result() (Result, error) {
	if coordinator == nil {
		return Result{}, errors.New("multisensor coordinator is nil")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.phase != phaseFinished || coordinator.result == nil {
		return Result{}, errors.New("multisensor coordinator has no complete result")
	}
	return cloneResult(*coordinator.result), nil
}

func (coordinator *Coordinator) Plan() Plan {
	if coordinator == nil {
		return Plan{}
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return clonePlan(coordinator.plan)
}

func (coordinator *Coordinator) requiredFailure(plan SourcePlan, err error) {
	if !plan.Required || err == nil {
		return
	}
	coordinator.failureOnce.Do(func() {
		coordinator.onRequiredFailure(fmt.Errorf("required source %q failed: %w", plan.SourceID, err))
	})
}

func (coordinator *Coordinator) abortAll(ctx context.Context) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.abortAllLocked(ctx)
}

func (coordinator *Coordinator) abortAllLocked(ctx context.Context) error {
	var result error
	for index := range coordinator.slots {
		slot := &coordinator.slots[index]
		if slot.worker != nil {
			result = errors.Join(result, slot.worker.abort(ctx))
		}
	}
	return result
}

type processProducer struct{ process *sensorproducer.Process }

func startProcessProducer(
	ctx context.Context,
	argv []string,
	sessionID string,
	sourceID string,
	options sensorproducer.ProcessOptions,
) (ProducerProcess, error) {
	process, err := sensorproducer.StartProcess(ctx, argv, sessionID, sourceID, options)
	if err != nil {
		return nil, err
	}
	return &processProducer{process: process}, nil
}

func (producer *processProducer) Ready(ctx context.Context) error {
	return producer.process.Client().Ready(ctx)
}
func (producer *processProducer) Arm(ctx context.Context) error {
	return producer.process.Client().Arm(ctx)
}
func (producer *processProducer) Start(ctx context.Context) error {
	return producer.process.Client().Start(ctx)
}
func (producer *processProducer) Stop(ctx context.Context) error {
	return producer.process.Client().Stop(ctx)
}
func (producer *processProducer) Cancel(ctx context.Context) error {
	return producer.process.Cancel(ctx)
}
func (producer *processProducer) Next(ctx context.Context) (sensorproducer.Record, error) {
	return producer.process.Client().Next(ctx)
}
func (producer *processProducer) Wait(ctx context.Context) error { return producer.process.Wait(ctx) }
func (producer *processProducer) Kill() error                    { return producer.process.Kill() }

func cloneResult(result Result) Result {
	result.ApplicationMetadata = cloneMetadata(result.ApplicationMetadata)
	sources := result.Sources
	result.Sources = make([]multisensor.Source, len(sources))
	for index := range result.Sources {
		result.Sources[index] = cloneSource(sources[index])
	}
	return result
}

func clonePlan(plan Plan) Plan {
	result := plan
	result.ApplicationMetadata = cloneMetadata(plan.ApplicationMetadata)
	result.Sources = append([]SourcePlan(nil), plan.Sources...)
	for index := range result.Sources {
		result.Sources[index].Argv = append([]string(nil), plan.Sources[index].Argv...)
		result.Sources[index].ApplicationMetadata = cloneMetadata(plan.Sources[index].ApplicationMetadata)
	}
	return result
}

func cloneSource(source multisensor.Source) multisensor.Source {
	result := source
	result.ClockObservations = append([]multisensor.ClockObservation(nil), source.ClockObservations...)
	result.AffineSegments = append([]multisensor.AffineSegment(nil), source.AffineSegments...)
	for index := range result.AffineSegments {
		result.AffineSegments[index].ObservationIDs = append(
			[]string(nil), source.AffineSegments[index].ObservationIDs...,
		)
	}
	result.Artifacts = append([]multisensor.Artifact(nil), source.Artifacts...)
	result.ApplicationMetadata = cloneMetadata(source.ApplicationMetadata)
	if source.SyncEventCardinality != nil {
		cardinality := *source.SyncEventCardinality
		result.SyncEventCardinality = &cardinality
	}
	return result
}

func cloneMetadata(metadata multisensor.ApplicationMetadata) multisensor.ApplicationMetadata {
	if metadata == nil {
		return nil
	}
	result := make(multisensor.ApplicationMetadata, len(metadata))
	for key, value := range metadata {
		result[key] = append([]byte(nil), value...)
	}
	return result
}

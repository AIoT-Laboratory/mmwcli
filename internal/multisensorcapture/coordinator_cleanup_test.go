package multisensorcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/sensorproducer"
)

func TestCoordinatorPublishesWithOptionalDataFailure(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	required := newFakeProducer(testRecords(t, [][]byte{{1, 2, 3}}, false, limits))
	plan := requiredOptionalPlan(limits)
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{4, 5, 6}}, true, limits), "camera-1", plan.Sources[1].Clock,
	))
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, optional)
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatalf("Finish with failed optional stream: %v", err)
	}
	sources, err := coordinator.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Outcome != multisensor.OutcomeComplete ||
		sources[1].Outcome != multisensor.OutcomeFailed {
		t.Fatalf("source outcomes = %+v", sources)
	}
}

func TestCoordinatorPublishesWithOptionalProcessExitFailure(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	sourceErr := errors.New("optional producer exited with status 1")
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, false, limits), "camera-1", plan.Sources[1].Clock,
	))
	optional.nextErr = sourceErr
	optional.waitErr = sourceErr
	optional.cancelErr = sourceErr
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, optional)
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatalf("Finish with optional process exit failure: %v", err)
	}
	sources, err := coordinator.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Outcome != multisensor.OutcomeComplete ||
		sources[1].Outcome != multisensor.OutcomeFailed {
		t.Fatalf("source outcomes = %+v", sources)
	}
	if calls := strings.Join(optional.callList(), ","); strings.Contains(calls, "CANCEL") {
		t.Fatalf("optional source was canceled after terminal stream failure: %s", calls)
	}
}
func TestCoordinatorPublishesWithOptionalArmFailureAndPlainCancelExit(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	sourceErr := errors.New("optional producer exited after ARM failure")
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, false, limits), "camera-1", plan.Sources[1].Clock,
	))
	optional.armErr = sourceErr
	optional.cancelErr = sourceErr
	optional.waitErr = sourceErr
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, optional)
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatalf("optional plain ARM/process failure should not reject aggregate: %v", err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatalf("Finish with optional plain cancel exit: %v", err)
	}
	sources, err := coordinator.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Outcome != multisensor.OutcomeComplete ||
		sources[1].Outcome != multisensor.OutcomeFailed {
		t.Fatalf("source outcomes = %+v", sources)
	}
	if calls := strings.Join(optional.callList(), ","); strings.Contains(calls, "KILL") {
		t.Fatalf("plain optional process exit incorrectly triggered kill: %s", calls)
	}
}
func TestCoordinatorRejectsOptionalWaitCleanupFailure(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	waitErr := errors.New("optional process did not converge")
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, false, limits), "camera-1", plan.Sources[1].Clock,
	))
	optional.waitErr = NewCleanupFailure("wait", waitErr)
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, optional)
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := coordinator.Finish(context.Background(), true)
	if !errors.Is(err, waitErr) || !strings.Contains(err.Error(), "producer cleanup wait") {
		t.Fatalf("Finish error = %v", err)
	}
	if _, resultErr := coordinator.Result(); resultErr == nil {
		t.Fatal("Result succeeded after optional cleanup failure")
	}
}

func TestCoordinatorRejectsOptionalStopAndActiveCancelTimeout(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	stopErr := errors.New("optional STOP rejected")
	cancelCause := context.DeadlineExceeded
	cancelErr := NewCleanupFailure("cancel control", cancelCause)
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, false, limits), "camera-1", plan.Sources[1].Clock,
	))
	optional.stopErr = stopErr
	optional.cancelErr = cancelErr
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, optional)
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := coordinator.Finish(context.Background(), true)
	if !errors.Is(err, stopErr) || !errors.Is(err, cancelCause) ||
		!strings.Contains(err.Error(), "producer cleanup cancel") {
		t.Fatalf("Finish error = %v", err)
	}
	if _, resultErr := coordinator.Result(); resultErr == nil {
		t.Fatal("Result succeeded after optional STOP cleanup failure")
	}
	if calls := strings.Join(optional.callList(), ","); strings.Contains(calls, "KILL") {
		t.Fatalf("coordinator issued a duplicate kill after producer-owned cancellation: %s", calls)
	}
}

func TestCoordinatorJoinsAbortCleanupAfterRequiredFailure(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	plan.Sources[1].Required = true
	requiredFailure := errors.New("required STOP failed")
	cleanupFailure := errors.New("second source did not converge")
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	required.stopErr = requiredFailure
	second := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, false, limits), "camera-1", plan.Sources[1].Clock,
	))
	second.cancelErr = NewCleanupFailure("cancel", cleanupFailure)
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, second)
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := coordinator.Finish(context.Background(), true)
	if !errors.Is(err, requiredFailure) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("Finish did not retain required and abort failures: %v", err)
	}
	if _, resultErr := coordinator.Result(); resultErr == nil {
		t.Fatal("Result succeeded after required failure")
	}
}

func TestWorkerCompletionWinsExpiredContext(t *testing.T) {
	producer := newFakeProducer(nil)
	worker := &sourceWorker{process: producer, done: make(chan struct{})}
	close(worker.done)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForDrain(worker, expired); err != nil {
		t.Fatalf("waitForDrain rejected completed worker: %v", err)
	}
	if _, err := worker.collect(expired); err != nil {
		t.Fatalf("collect rejected completed worker: %v", err)
	}
	if calls := strings.Join(producer.callList(), ","); calls != "WAIT" {
		t.Fatalf("completed collect lifecycle = %s, want WAIT", calls)
	}
}

func TestCoordinatorRejectsOptionalFailureWhenSourceRemovalFails(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, true, limits), "camera-1", plan.Sources[1].Clock,
	))
	coordinator, _ := newRequiredOptionalCoordinator(t, plan, required, optional)
	removeErr := errors.New("source directory remains visible")
	coordinator.removeSource = func(*multisensor.Directory, string) error { return removeErr }
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := coordinator.Finish(context.Background(), true)
	if !errors.Is(err, removeErr) {
		t.Fatalf("Finish ignored source removal failure: %v", err)
	}
	if _, resultErr := coordinator.Result(); resultErr == nil {
		t.Fatal("Result succeeded while a failed source directory remained")
	}
}

func TestCoordinatorClassifiesProductionProcessTerminalPaths(t *testing.T) {
	t.Run("ordinary optional terminal failure", func(t *testing.T) {
		coordinator := newProcessCoordinator(t, "terminal-failure")
		if err := coordinator.Arm(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Finish(context.Background(), true); err != nil {
			t.Fatalf("Finish rejected ordinary optional process failure: %v", err)
		}
		assertSourceOutcomes(t, coordinator, multisensor.OutcomeComplete, multisensor.OutcomeFailed)
	})

	t.Run("unproven process convergence rejects result", func(t *testing.T) {
		coordinator := newProcessCoordinator(t, "delayed-exit")
		if err := coordinator.Arm(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		finishCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := coordinator.Finish(finishCtx, true)
		if cleanupFailureOf(err) == nil || !sensorproducer.IsConvergenceError(err) {
			t.Fatalf("Finish convergence classification = %v", err)
		}
		if _, resultErr := coordinator.Result(); resultErr == nil {
			t.Fatal("Result succeeded after unproven optional process convergence")
		}
	})

	t.Run("cancel and natural exit race remains an optional failure", func(t *testing.T) {
		coordinator := newProcessCoordinator(t, "arm-reject-exit")
		if err := coordinator.Arm(context.Background()); err != nil {
			t.Fatalf("Arm rejected aggregate during optional exit race: %v", err)
		}
		if err := coordinator.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Finish(context.Background(), true); err != nil {
			t.Fatalf("Finish rejected reaped optional exit race: %v", err)
		}
		assertSourceOutcomes(t, coordinator, multisensor.OutcomeComplete, multisensor.OutcomeFailed)
	})
}

func newProcessCoordinator(t *testing.T, optionalMode string) *Coordinator {
	t.Helper()
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 8, MaxPayloadBytes: 16}
	plan := requiredOptionalPlan(limits)
	plan.Sources[0].Argv = coordinatorProcessCommand("complete")
	plan.Sources[1].Argv = coordinatorProcessCommand(optionalMode)
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := Start(context.Background(), plan, testSessionID, directory, Options{
		Stderr: io.Discard, OnRequiredFailure: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func assertSourceOutcomes(t *testing.T, coordinator *Coordinator, outcomes ...multisensor.SourceOutcome) {
	t.Helper()
	sources, err := coordinator.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != len(outcomes) {
		t.Fatalf("source count = %d, want %d", len(sources), len(outcomes))
	}
	for index, outcome := range outcomes {
		if sources[index].Outcome != outcome {
			t.Fatalf("source %d outcome = %q, want %q", index, sources[index].Outcome, outcome)
		}
	}
}

func TestCoordinatorProcessHelper(t *testing.T) {
	mode, ok := coordinatorProcessMode(os.Args)
	if !ok {
		return
	}
	if err := runCoordinatorProcessHelper(mode); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func coordinatorProcessCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestCoordinatorProcessHelper$", "--", mode}
}

func coordinatorProcessMode(arguments []string) (string, bool) {
	for index, argument := range arguments {
		if argument == "--" && index+1 < len(arguments) {
			return arguments[index+1], true
		}
	}
	return "", false
}

func runCoordinatorProcessHelper(mode string) error {
	controls, err := sensorproducer.NewControlDecoder(os.Stdin)
	if err != nil {
		return err
	}
	frames, err := sensorproducer.NewEncoder(os.Stdout)
	if err != nil {
		return err
	}
	var dataSeq uint64
	for {
		control, err := controls.Read()
		if err != nil {
			return err
		}
		accepted := !(mode == "arm-reject-exit" && control.Command == sensorproducer.CommandArm)
		message := ""
		if !accepted {
			message = "intentional ARM rejection"
		}
		ack, err := sensorproducer.NewACKRecord(control, accepted, message)
		if err != nil {
			return err
		}
		if err := frames.Write(ack); err != nil {
			return err
		}
		if !accepted {
			return nil
		}
		sourcePlan, err := coordinatorHelperSourcePlan(control.SourceID)
		if err != nil {
			return err
		}
		switch control.Command {
		case sensorproducer.CommandStart:
			payload := []byte{1, 2, 3, 4}
			dataSeq++
			if err := writeCoordinatorProcessRecord(
				frames, sensorproducer.FrameSession, control, dataSeq, coordinatorHelperSession(sourcePlan), nil,
			); err != nil {
				return err
			}
			dataSeq++
			if err := writeCoordinatorProcessRecord(frames, sensorproducer.FrameItem, control, dataSeq, ProducerItemMetadata{
				Schema: ProducerItemSchema, ItemIndex: 0, Tick: 100, DurationTicks: 1,
				SyncEventID: multisensor.NoSyncEventID,
			}, payload); err != nil {
				return err
			}
			if mode == "terminal-failure" {
				return errors.New("intentional producer terminal failure")
			}
		case sensorproducer.CommandStop:
			payload := []byte{1, 2, 3, 4}
			digest := sha256.Sum256(payload)
			dataSeq++
			if err := writeCoordinatorProcessRecord(frames, sensorproducer.FrameEnd, control, dataSeq, ProducerEndMetadata{
				Schema: ProducerEndSchema, ItemCount: 1, PayloadBytes: uint64(len(payload)),
				PayloadSHA256: hex.EncodeToString(digest[:]),
			}, nil); err != nil {
				return err
			}
			dataSeq++
			if err := writeCoordinatorProcessRecord(
				frames, sensorproducer.FrameEOF, control, dataSeq, ProducerEOFMetadata{Schema: ProducerEOFSchema}, nil,
			); err != nil {
				return err
			}
			if mode == "delayed-exit" {
				if err := os.Stdout.Close(); err != nil {
					return err
				}
				time.Sleep(500 * time.Millisecond)
			}
			return nil
		case sensorproducer.CommandCancel:
			dataSeq++
			return writeCoordinatorProcessRecord(
				frames, sensorproducer.FrameEOF, control, dataSeq, ProducerEOFMetadata{Schema: ProducerEOFSchema}, nil,
			)
		}
	}
}

func coordinatorHelperSourcePlan(sourceID string) (SourcePlan, error) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 8, MaxPayloadBytes: 16}
	for _, source := range requiredOptionalPlan(limits).Sources {
		if source.SourceID == sourceID {
			return source, nil
		}
	}
	return SourcePlan{}, fmt.Errorf("unknown helper source %q", sourceID)
}

func coordinatorHelperSession(plan SourcePlan) ProducerSessionMetadata {
	return ProducerSessionMetadata{
		Schema: ProducerSessionSchema, Kind: plan.Kind, Producer: plan.Producer,
		Limits: plan.Limits, Payload: plan.Payload, Clock: plan.Clock,
		ClockObservations: []multisensor.ClockObservation{
			{ObservationID: "obs-0", Tick: 100, HostBeforeNS: 1_000_000, HostAfterNS: 1_000_000},
		},
		AffineSegments: []multisensor.AffineSegment{
			{
				StartUnwrappedTick: 0, EndUnwrappedTick: 1_000, SourceOriginTick: 100,
				HostOriginNS: 1_000_000, ScaleNum: 1_000, ScaleDen: 1,
				ObservationIDs: []string{"obs-0"},
			},
		},
		SyncEventSemantics: plan.SyncEventSemantics, ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
}

func writeCoordinatorProcessRecord(
	frames *sensorproducer.Encoder,
	frameType sensorproducer.FrameType,
	control sensorproducer.Control,
	sequence uint64,
	metadata any,
	payload []byte,
) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return frames.Write(sensorproducer.Record{
		Type: frameType, SessionID: control.SessionID, SourceID: control.SourceID,
		Seq: sequence, Metadata: encoded, Payload: payload,
	})
}

func TestCoordinatorRejectsOptionalReadyCleanupFailure(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	plan := requiredOptionalPlan(limits)
	required := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	readyErr := errors.New("optional READY rejected")
	cancelCause := errors.New("optional READY cleanup failed")
	cancelErr := NewCleanupFailure("cancel control", cancelCause)
	optional := newFakeProducer(recordsForSource(
		t, testRecords(t, [][]byte{{2}}, false, limits), "camera-1", plan.Sources[1].Clock,
	))
	optional.readyErr = readyErr
	optional.cancelErr = cancelErr
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := Start(context.Background(), plan, testSessionID, directory, Options{
		StartProducer: fakeStarterBySource(map[string]*fakeProducer{
			"camera-0": required,
			"camera-1": optional,
		}),
		OnRequiredFailure: func(error) {},
	})
	if coordinator != nil || !errors.Is(err, readyErr) || !errors.Is(err, cancelCause) ||
		!strings.Contains(err.Error(), "producer cleanup cancel") {
		t.Fatalf("Start coordinator=%v error=%v", coordinator, err)
	}
}

func TestCoordinatorRequiredHealthyControl(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	coordinator, _ := newTestCoordinator(t, newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits)), true, limits, Options{
		OnRequiredFailure: func(error) { t.Error("unexpected required source failure") },
	})
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
}

func requiredOptionalPlan(limits multisensor.SourceLimits) Plan {
	plan := validPlan(true, limits)
	optional := plan.Sources[0]
	optional.SourceID = "camera-1"
	optional.Required = false
	optional.Argv = []string{"fake-camera-1"}
	optional.Clock.ClockID = "camera-1-clock"
	plan.Sources = append(plan.Sources, optional)
	return plan
}

func newRequiredOptionalCoordinator(
	t *testing.T,
	plan Plan,
	required *fakeProducer,
	optional *fakeProducer,
) (*Coordinator, *multisensor.Directory) {
	t.Helper()
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := Start(context.Background(), plan, testSessionID, directory, Options{
		StartProducer: fakeStarterBySource(map[string]*fakeProducer{
			"camera-0": required,
			"camera-1": optional,
		}),
		OnRequiredFailure: func(error) { t.Error("unexpected required source failure") },
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, directory
}

func recordsForSource(
	t *testing.T,
	records []sensorproducer.Record,
	sourceID string,
	clock multisensor.Clock,
) []sensorproducer.Record {
	t.Helper()
	result := append([]sensorproducer.Record(nil), records...)
	for index := range result {
		result[index].SourceID = sourceID
	}
	var metadata ProducerSessionMetadata
	if err := json.Unmarshal(result[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.Clock = clock
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	result[0].Metadata = encoded
	return result
}

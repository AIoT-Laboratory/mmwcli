package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
	"mmwcli/internal/multisensorstream"
	"mmwcli/internal/radar"
)

type fakeAggregateCoordinator struct {
	result   multisensorcapture.Result
	finishes []bool
}

func (*fakeAggregateCoordinator) Arm(context.Context) error   { return nil }
func (*fakeAggregateCoordinator) Start(context.Context) error { return nil }
func (coordinator *fakeAggregateCoordinator) Finish(_ context.Context, complete bool) error {
	coordinator.finishes = append(coordinator.finishes, complete)
	return nil
}
func (coordinator *fakeAggregateCoordinator) Result() (multisensorcapture.Result, error) {
	return coordinator.result, nil
}

func TestCaptureCommandsExposeMultisensorPlan(t *testing.T) {
	for _, command := range []string{"studio-cli", "debug-cli"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run([]string{command, "capture", "--help"}, &stdout, &stderr); code != 0 {
				t.Fatalf("help exit = %d: %s%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), "multisensor-plan") {
				t.Fatalf("capture help omits --multisensor-plan: %s%s", stdout.String(), stderr.String())
			}
		})
	}
}

func TestActiveMultisensorStreamPublishesRadarAndCameraItems(t *testing.T) {
	prepared, err := loadCaptureOutputPlan(radar.StudioCLI, writeValidConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	directory, radarDirectory, outputPath := createAggregateTestDirectories(t, prepared)
	sessionID, err := multisensor.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	plan := aggregateStreamTestPlan()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readResult := readAggregateStream(reader)
	stream, err := startMultisensorStream(
		context.Background(), writer, radarDirectory, prepared, prepared.plan.ExpectedBytes,
		plan, sessionID, func() {},
	)
	if err != nil {
		t.Fatal(err)
	}
	external := plan.Sources[0]
	coordinator := &fakeAggregateCoordinator{result: aggregateTestResult(sessionID, []multisensor.Source{{
		SourceID: external.SourceID, Kind: external.Kind, Required: false,
		Outcome: multisensor.OutcomeFailed, Producer: external.Producer, Limits: external.Limits,
		Payload: external.Payload, Clock: external.Clock,
		ClockObservations: []multisensor.ClockObservation{}, AffineSegments: []multisensor.AffineSegment{},
		Artifacts: []multisensor.Artifact{}, ApplicationMetadata: multisensor.ApplicationMetadata{},
	}})}
	origin := time.Now()
	capture := &activeMultisensorCapture{
		directory: directory, radarDirectory: radarDirectory, coordinator: coordinator,
		sessionID: sessionID, prepared: prepared, hostOrigin: origin, stream: stream,
	}

	cameraPayload := []byte{0, 1, 0xff, 2}
	if err := stream.adapter.WriteItem(context.Background(), multisensorstream.Item{
		SourceID: "camera-0", ItemIndex: 0, Tick: 100, DurationTicks: 1,
		SyncEventID: multisensorstream.NoSyncEventID, Payload: cameraPayload,
	}); err != nil {
		t.Fatal(err)
	}
	radarPayload := make([]byte, int(prepared.plan.ExpectedBytes))
	for index := range radarPayload {
		radarPayload[index] = byte(index % 251)
	}
	if written, err := stream.mirror.WriteAt(radarPayload, 0); err != nil || written != len(radarPayload) {
		t.Fatalf("write radar mirror = %d, %v", written, err)
	}
	capture.RadarFrameStartObserved(origin.Add(time.Millisecond), origin.Add(2*time.Millisecond))
	sealCtx, cancelSeal := context.WithTimeout(context.Background(), time.Second)
	if err := stream.mirror.Seal(sealCtx); err != nil {
		cancelSeal()
		t.Fatal(err)
	}
	cancelSeal()
	if err := radarDirectory.Truncate(prepared.plan.ExpectedBytes); err != nil {
		t.Fatal(err)
	}
	if err := radarDirectory.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := capture.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := capture.finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	records, readErr := awaitAggregateStream(t, readResult)
	if readErr != nil {
		t.Fatal(readErr)
	}
	wantTypes := []multisensorstream.RecordType{
		multisensorstream.RecordSession, multisensorstream.RecordRadarConfig,
		multisensorstream.RecordItem, multisensorstream.RecordRadarStart, multisensorstream.RecordItem,
		multisensorstream.RecordEnd, multisensorstream.RecordEnd,
		multisensorstream.RecordCommit, multisensorstream.RecordEOF,
	}
	if len(records) != len(wantTypes) {
		t.Fatalf("aggregate record count = %d", len(records))
	}
	for index, want := range wantTypes {
		if records[index].Type != want {
			t.Fatalf("aggregate record %d type = %d, want %d", index, records[index].Type, want)
		}
	}
	if string(records[2].Payload) != string(cameraPayload) || string(records[4].Payload) != string(radarPayload) {
		t.Fatal("aggregate ITEM payload differs from camera or radar authority")
	}
	var radarItem struct {
		SourceID      string `json:"source_id"`
		ItemIndex     uint64 `json:"item_index"`
		Tick          uint64 `json:"tick"`
		DurationTicks uint64 `json:"duration_ticks"`
	}
	if err := json.Unmarshal(records[4].Metadata, &radarItem); err != nil ||
		radarItem.SourceID != aggregateRadarSourceID || radarItem.ItemIndex != 0 ||
		radarItem.Tick != 0 || radarItem.DurationTicks != uint64(prepared.plan.FramePeriod) {
		t.Fatalf("radar ITEM time = %+v, %v", radarItem, err)
	}
	var radarStart struct {
		SourceID    string `json:"source_id"`
		HostLowerNS uint64 `json:"host_lower_ns"`
		HostUpperNS uint64 `json:"host_upper_ns"`
	}
	if err := json.Unmarshal(records[3].Metadata, &radarStart); err != nil ||
		radarStart.SourceID != aggregateRadarSourceID ||
		radarStart.HostLowerNS != uint64(time.Millisecond) ||
		radarStart.HostUpperNS != uint64(2*time.Millisecond) {
		t.Fatalf("RADAR_START = %+v, %v", radarStart, err)
	}
	var cameraEnd struct {
		SourceID string                    `json:"source_id"`
		Outcome  multisensor.SourceOutcome `json:"outcome"`
	}
	if err := json.Unmarshal(records[6].Metadata, &cameraEnd); err != nil ||
		cameraEnd.SourceID != "camera-0" || cameraEnd.Outcome != multisensor.OutcomeFailed {
		t.Fatalf("camera END = %+v, %v", cameraEnd, err)
	}
	if _, err := os.Stat(filepath.Join(outputPath, multisensor.SessionFileName)); err != nil {
		t.Fatalf("aggregate session was not published: %v", err)
	}
}

func TestOpenEndedMultisensorStreamEndsAtObservedRadarFrameCount(t *testing.T) {
	count := uint16(0)
	prepared, err := loadCaptureOutputPlanWithFrameCount(
		radar.StudioCLI,
		writeValidConfig(t),
		&count,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, radarDirectory, _ := createAggregateTestDirectories(t, prepared)
	sessionID, err := multisensor.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readResult := readAggregateStream(reader)
	stream, err := startMultisensorStream(
		context.Background(),
		writer,
		radarDirectory,
		prepared,
		3*prepared.plan.BytesPerFrame,
		aggregateStreamTestPlan(),
		sessionID,
		func() {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.adapter.WriteRadarStart(context.Background(), multisensorstream.RadarStart{
		SourceID: aggregateRadarSourceID, HostLowerNS: 1, HostUpperNS: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := stream.radarSink.ReleaseRadarStart(); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 2*int(prepared.plan.BytesPerFrame))
	if written, err := stream.mirror.WriteAt(payload, 0); err != nil || written != len(payload) {
		t.Fatalf("write open-ended radar mirror = %d, %v", written, err)
	}
	sealCtx, cancelSeal := context.WithTimeout(context.Background(), time.Second)
	if err := stream.mirror.Seal(sealCtx); err != nil {
		cancelSeal()
		t.Fatal(err)
	}
	cancelSeal()
	if err := stream.adapter.EndSource(
		context.Background(),
		aggregateRadarSourceID,
		multisensorstream.OutcomeComplete,
	); err != nil {
		t.Fatal(err)
	}
	if err := stream.adapter.EndSource(
		context.Background(),
		"camera-0",
		multisensorstream.OutcomeOmitted,
	); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("published-session"))
	if err := stream.adapter.Commit(context.Background(), multisensorstream.SessionArtifact{
		SizeBytes: uint64(len("published-session")), SHA256: digest,
	}); err != nil {
		t.Fatal(err)
	}
	if err := stream.adapter.Close(); err != nil {
		t.Fatal(err)
	}

	records, err := awaitAggregateStream(t, readResult)
	if err != nil {
		t.Fatal(err)
	}
	var radarItems int
	var radarEnd struct {
		SourceID  string `json:"source_id"`
		ItemCount uint64 `json:"item_count"`
	}
	var session struct {
		Sources []multisensorstream.Source `json:"sources"`
	}
	for _, record := range records {
		switch record.Type {
		case multisensorstream.RecordSession:
			if err := json.Unmarshal(record.Metadata, &session); err != nil {
				t.Fatal(err)
			}
		case multisensorstream.RecordItem:
			var item struct {
				SourceID string `json:"source_id"`
			}
			if err := json.Unmarshal(record.Metadata, &item); err != nil {
				t.Fatal(err)
			}
			if item.SourceID == aggregateRadarSourceID {
				radarItems++
			}
		case multisensorstream.RecordEnd:
			var end struct {
				SourceID  string `json:"source_id"`
				ItemCount uint64 `json:"item_count"`
			}
			if err := json.Unmarshal(record.Metadata, &end); err != nil {
				t.Fatal(err)
			}
			if end.SourceID == aggregateRadarSourceID {
				radarEnd = end
			}
		}
	}
	if len(session.Sources) == 0 || session.Sources[0].Limits.MaxItems != 3 ||
		session.Sources[0].Limits.MaxPayloadBytes != uint64(3*prepared.plan.BytesPerFrame) {
		t.Fatalf("open-ended radar SESSION limits = %+v", session.Sources)
	}
	if radarItems != 2 || radarEnd.SourceID != aggregateRadarSourceID || radarEnd.ItemCount != 2 {
		t.Fatalf("open-ended radar items=%d END=%+v", radarItems, radarEnd)
	}
}

func TestActiveMultisensorStreamRequiredFailureEmitsAbortAndEOF(t *testing.T) {
	prepared, err := loadCaptureOutputPlan(radar.StudioCLI, writeValidConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	directory, radarDirectory, outputPath := createAggregateTestDirectories(t, prepared)
	sessionID, err := multisensor.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readResult := readAggregateStream(reader)
	stream, err := startMultisensorStream(
		context.Background(), writer, radarDirectory, prepared, prepared.plan.ExpectedBytes,
		aggregateStreamTestPlan(), sessionID, func() {},
	)
	if err != nil {
		t.Fatal(err)
	}
	blockedRadar := make([]byte, int(prepared.plan.ExpectedBytes))
	if written, err := stream.mirror.WriteAt(blockedRadar, 0); err != nil || written != len(blockedRadar) {
		t.Fatalf("write blocked radar mirror = %d, %v", written, err)
	}
	coordinator := &fakeAggregateCoordinator{}
	capture := &activeMultisensorCapture{
		directory: directory, radarDirectory: radarDirectory, coordinator: coordinator, stream: stream,
	}
	wantErr := errors.New("required camera failed")
	capture.recordRequiredFailure(wantErr)
	if err := capture.finish(context.Background(), wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v", err)
	}
	records, readErr := awaitAggregateStream(t, readResult)
	if readErr != nil {
		t.Fatal(readErr)
	}
	wantTypes := []multisensorstream.RecordType{
		multisensorstream.RecordSession, multisensorstream.RecordRadarConfig,
		multisensorstream.RecordAbort, multisensorstream.RecordEOF,
	}
	if len(records) != len(wantTypes) {
		t.Fatalf("failure record count = %d", len(records))
	}
	for index, want := range wantTypes {
		if records[index].Type != want {
			t.Fatalf("failure record %d type = %d, want %d", index, records[index].Type, want)
		}
	}
	var abort struct {
		Reason multisensorstream.AbortReason `json:"reason_code"`
	}
	if err := json.Unmarshal(records[2].Metadata, &abort); err != nil ||
		abort.Reason != multisensorstream.AbortSourceFailed {
		t.Fatalf("ABORT = %+v, %v", abort, err)
	}
	if _, err := os.Stat(outputPath + ".part"); err != nil {
		t.Fatalf("failed aggregate stage missing: %v", err)
	}
}

func TestActiveMultisensorCapturePublishesRadarAggregateAfterParticipantFinish(t *testing.T) {
	prepared, err := loadCaptureOutputPlan(radar.StudioCLI, writeValidConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "aggregate")
	directory, err := multisensor.CreateDirectory(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	radarPath, err := directory.SourcePath(aggregateRadarSourceID)
	if err != nil {
		t.Fatal(err)
	}
	radarDirectory, err := createCaptureOutput(radarPath, prepared.finalizeSession)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, int(prepared.plan.ExpectedBytes))
	if written, err := radarDirectory.WriteAt(payload, 0); err != nil || written != len(payload) {
		t.Fatalf("write radar payload = %d, %v", written, err)
	}
	if err := radarDirectory.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessionID, err := multisensor.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeAggregateCoordinator{result: multisensorcapture.Result{
		PlanSchema: multisensorcapture.PlanSchema, SessionID: sessionID,
		SynchronizationGrade: multisensor.SynchronizationSoftwareBarrier,
		HostClock: multisensor.Clock{
			ClockID: multisensor.HostClockID, TickHz: 1_000_000_000,
			TimestampSemantics: multisensor.TimestampHostMonotonic,
		},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
		Sources:             []multisensor.Source{},
	}}
	origin := time.Now()
	capture := &activeMultisensorCapture{
		directory: directory, radarDirectory: radarDirectory, coordinator: coordinator,
		sessionID: sessionID, prepared: prepared, hostOrigin: origin,
	}
	capture.RadarFrameStartObserved(origin.Add(10*time.Millisecond), origin.Add(12*time.Millisecond))
	if err := capture.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := capture.finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !directory.Committed() || len(coordinator.finishes) != 1 || !coordinator.finishes[0] {
		t.Fatalf("aggregate completion = committed:%v finishes:%v", directory.Committed(), coordinator.finishes)
	}
	encoded, err := os.ReadFile(filepath.Join(outputPath, multisensor.SessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := multisensor.UnmarshalSession(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionRecord.Sources) != 1 || sessionRecord.Sources[0].SourceID != aggregateRadarSourceID {
		t.Fatalf("aggregate sources = %+v", sessionRecord.Sources)
	}
	observation := sessionRecord.Sources[0].ClockObservations[0]
	if observation.HostBeforeNS != uint64(10*time.Millisecond) ||
		observation.HostAfterNS != uint64(12*time.Millisecond) {
		t.Fatalf("radar frame-start observation = %+v", observation)
	}
	for _, name := range []string{"adc.bin", "capture.json", "index.bin", "radar.cfg"} {
		if _, err := os.Stat(filepath.Join(outputPath, "sensors", aggregateRadarSourceID, name)); err != nil {
			t.Fatalf("aggregate radar artifact %q: %v", name, err)
		}
	}
}

func TestActiveMultisensorCaptureFailureAbortsParticipantAndRetainsStage(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "aggregate")
	directory, err := multisensor.CreateDirectory(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeAggregateCoordinator{}
	capture := &activeMultisensorCapture{directory: directory, coordinator: coordinator}
	wantErr := errors.New("radar failed")
	if err := capture.finish(context.Background(), wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v", err)
	}
	if len(coordinator.finishes) != 1 || coordinator.finishes[0] {
		t.Fatalf("participant finishes = %v", coordinator.finishes)
	}
	if _, err := os.Stat(outputPath + ".part"); err != nil {
		t.Fatalf("failed aggregate stage missing: %v", err)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed aggregate published: %v", err)
	}
}

func createAggregateTestDirectories(
	t *testing.T,
	prepared preparedCaptureOutput,
) (*multisensor.Directory, *capturefile.SessionDirectory, string) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "aggregate")
	directory, err := multisensor.CreateDirectory(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	radarPath, err := directory.SourcePath(aggregateRadarSourceID)
	if err != nil {
		t.Fatal(err)
	}
	radarDirectory, err := createCaptureOutput(radarPath, prepared.finalizeSession)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = radarDirectory.Close() })
	return directory, radarDirectory, outputPath
}

func aggregateStreamTestPlan() multisensorcapture.Plan {
	return multisensorcapture.Plan{
		Schema: multisensorcapture.PlanSchema,
		Sources: []multisensorcapture.SourcePlan{{
			SourceID: "camera-0", Kind: multisensor.SourceCamera, Required: false,
			Argv: []string{"fake-camera"}, QueueSize: 1,
			Producer: multisensor.Producer{Name: "fake-camera", Version: "1.0"},
			Limits:   multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 16, MaxPayloadBytes: 32},
			Payload:  multisensor.PayloadContract{Filename: "frames.bin", Format: "camera.raw.v1"},
			Clock: multisensor.Clock{
				ClockID: "camera-0-clock", TickHz: 1_000_000,
				TimestampSemantics: multisensor.TimestampExposureMidpoint,
			},
			SyncEventSemantics:  multisensorcapture.SyncEventSemanticsNone,
			ApplicationMetadata: multisensor.ApplicationMetadata{},
		}},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
}

func aggregateTestResult(sessionID string, sources []multisensor.Source) multisensorcapture.Result {
	return multisensorcapture.Result{
		PlanSchema: multisensorcapture.PlanSchema, SessionID: sessionID,
		SynchronizationGrade: multisensor.SynchronizationSoftwareBarrier,
		HostClock: multisensor.Clock{
			ClockID: multisensor.HostClockID, TickHz: uint64(time.Second),
			TimestampSemantics: multisensor.TimestampHostMonotonic,
		},
		ApplicationMetadata: multisensor.ApplicationMetadata{}, Sources: sources,
	}
}

type aggregateStreamReadResult struct {
	records []multisensorstream.Record
	err     error
}

func readAggregateStream(reader *os.File) <-chan aggregateStreamReadResult {
	result := make(chan aggregateStreamReadResult, 1)
	go func() {
		defer reader.Close()
		decoder, err := multisensorstream.NewDecoder(reader)
		if err != nil {
			result <- aggregateStreamReadResult{err: err}
			return
		}
		var records []multisensorstream.Record
		for {
			record, readErr := decoder.Read()
			if errors.Is(readErr, io.EOF) {
				result <- aggregateStreamReadResult{records: records}
				return
			}
			if readErr != nil {
				result <- aggregateStreamReadResult{records: records, err: readErr}
				return
			}
			records = append(records, record)
		}
	}()
	return result
}

func awaitAggregateStream(
	t *testing.T,
	result <-chan aggregateStreamReadResult,
) ([]multisensorstream.Record, error) {
	t.Helper()
	select {
	case value := <-result:
		return value.records, value.err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for aggregate stream EOF")
		return nil, nil
	}
}

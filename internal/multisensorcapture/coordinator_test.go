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
	"sync"
	"testing"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorstream"
	"mmwcli/internal/sensorproducer"
)

const testSessionID = "123e4567-e89b-42d3-a456-426614174000"

func TestCoordinatorRecordsCompleteExternalSource(t *testing.T) {
	payloads := [][]byte{{1, 2, 3}, {4, 5}}
	limits := multisensor.SourceLimits{MaxItems: 4, MaxItemBytes: 16, MaxPayloadBytes: 64}
	producer := newFakeProducer(testRecords(t, payloads, false, limits))
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := directory.SourcePath("camera-0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingItemSink{payloadPath: filepath.Join(sourcePath, "frames.bin")}
	coordinator, err := Start(context.Background(), validPlan(true, limits), testSessionID, directory, Options{
		StartProducer: fakeStarter(producer), ItemSink: sink, OnRequiredFailure: func(error) {
			t.Error("unexpected required-source failure callback")
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start participant: %v", err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	result, err := coordinator.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if result.PlanSchema != PlanSchema || result.SessionID != testSessionID ||
		result.SynchronizationGrade != multisensor.SynchronizationSoftwareBarrier || len(result.Sources) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	source := result.Sources[0]
	if source.Outcome != multisensor.OutcomeComplete || source.ItemCount != 2 || source.PayloadBytes != 5 {
		t.Fatalf("unexpected source: %+v", source)
	}
	payload, err := os.ReadFile(filepath.Join(sourcePath, "frames.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string([]byte{1, 2, 3, 4, 5}) {
		t.Fatalf("payload = %v", payload)
	}
	indexBytes, err := os.ReadFile(filepath.Join(sourcePath, multisensor.IndexFileName))
	if err != nil {
		t.Fatal(err)
	}
	index, err := multisensor.DecodeSensorIndex(indexBytes, source.Limits)
	if err != nil {
		t.Fatalf("DecodeSensorIndex: %v", err)
	}
	if len(index.Entries) != 2 || index.Entries[1].PayloadOffset != 3 || index.PayloadBytes != 5 {
		t.Fatalf("unexpected index: %+v", index)
	}
	items, authoritativePayloads, sinkErr := sink.snapshot()
	if sinkErr != nil {
		t.Fatal(sinkErr)
	}
	if len(items) != 2 || string(items[0].Payload) != string(payloads[0]) ||
		string(items[1].Payload) != string(payloads[1]) {
		t.Fatalf("sink items = %+v", items)
	}
	if items[1].SourceID != "camera-0" || items[1].ItemIndex != index.Entries[1].ItemIndex ||
		items[1].Tick != index.Entries[1].Tick || items[1].WrapCount != index.Entries[1].WrapCount ||
		items[1].DurationTicks != index.Entries[1].DurationTicks ||
		items[1].SyncEventID != index.Entries[1].SyncEventID {
		t.Fatalf("sink item does not match authoritative index: item=%+v index=%+v", items[1], index.Entries[1])
	}
	if len(authoritativePayloads) != 2 || string(authoritativePayloads[0]) != string(payloads[0]) ||
		string(authoritativePayloads[1]) != string(payload) {
		t.Fatalf("authority snapshots before sink = %v", authoritativePayloads)
	}
	if calls := producer.callList(); strings.Join(calls, ",") != "READY,ARM,START,STOP,WAIT" {
		t.Fatalf("producer calls = %v", calls)
	}
	// Returned contracts are safe for the app to merge with its radar source.
	result.Sources[0].Artifacts[0].SHA256 = "mutated"
	sources, err := coordinator.Sources()
	if err != nil || sources[0].Artifacts[0].SHA256 == "mutated" {
		t.Fatalf("Sources clone = %+v, err=%v", sources, err)
	}
}

func TestCoordinatorAssignsDeliveryObservedCameraTime(t *testing.T) {
	payloads := [][]byte{{1, 2, 3}, {4, 5}}
	limits := multisensor.SourceLimits{MaxItems: 4, MaxItemBytes: 16, MaxPayloadBytes: 64}
	producer := newFakeProducer(deliveryObservedRecords(t, payloads, limits))
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := directory.SourcePath("camera-0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingItemSink{payloadPath: filepath.Join(sourcePath, "frames.bin")}
	hostOrigin := time.Now().Add(-time.Second)
	lowerTick := uint64(time.Since(hostOrigin))
	coordinator, err := Start(context.Background(), deliveryObservedPlan(true, limits), testSessionID, directory, Options{
		HostOrigin: hostOrigin, StartProducer: fakeStarter(producer), ItemSink: sink,
		OnRequiredFailure: func(error) { t.Error("unexpected required-source failure callback") },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start participant: %v", err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	upperTick := uint64(time.Since(hostOrigin))
	sources, err := coordinator.Sources()
	if err != nil {
		t.Fatal(err)
	}
	source := sources[0]
	if source.Clock.ClockID != multisensor.DeliveryObservedClockID(source.SourceID) ||
		source.Clock.TickHz != uint64(time.Second) || source.Clock.WrapTicks != 0 ||
		source.Clock.TimestampSemantics != multisensor.TimestampDeliveryObserved {
		t.Fatalf("delivery clock = %+v", source.Clock)
	}
	indexBytes, err := os.ReadFile(filepath.Join(sourcePath, multisensor.IndexFileName))
	if err != nil {
		t.Fatal(err)
	}
	index, err := multisensor.DecodeSensorIndex(indexBytes, source.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != len(payloads) {
		t.Fatalf("index entries = %+v", index.Entries)
	}
	for entryIndex, entry := range index.Entries {
		if entry.Tick < lowerTick || entry.Tick > upperTick || entry.WrapCount != 0 || entry.DurationTicks != 0 {
			t.Fatalf("index entry %d has invalid delivery time: %+v, bounds=[%d,%d]", entryIndex, entry, lowerTick, upperTick)
		}
		if entryIndex > 0 && entry.Tick < index.Entries[entryIndex-1].Tick {
			t.Fatalf("delivery ticks move backwards: %+v", index.Entries)
		}
	}
	items, authoritativePayloads, sinkErr := sink.snapshot()
	if sinkErr != nil {
		t.Fatal(sinkErr)
	}
	if len(items) != len(index.Entries) || len(authoritativePayloads) != len(index.Entries) {
		t.Fatalf("sink items=%d authority snapshots=%d", len(items), len(authoritativePayloads))
	}
	for itemIndex, item := range items {
		entry := index.Entries[itemIndex]
		if item.Tick != entry.Tick || item.WrapCount != 0 || item.DurationTicks != 0 ||
			string(item.Payload) != string(payloads[itemIndex]) {
			t.Fatalf("sink item %d = %+v, index = %+v", itemIndex, item, entry)
		}
	}
	if len(source.ClockObservations) != 1 || len(source.AffineSegments) != 1 {
		t.Fatalf("delivery mapping = observations %+v, segments %+v", source.ClockObservations, source.AffineSegments)
	}
	firstTick := index.Entries[0].Tick
	lastTick := index.Entries[len(index.Entries)-1].Tick
	observation := source.ClockObservations[0]
	segment := source.AffineSegments[0]
	if observation.ObservationID != "camera-0-delivery-anchor" || observation.Tick != firstTick ||
		observation.HostBeforeNS != firstTick || observation.HostAfterNS != firstTick {
		t.Fatalf("delivery observation = %+v", observation)
	}
	if segment.StartUnwrappedTick != firstTick || segment.EndUnwrappedTick != lastTick+1 ||
		segment.SourceOriginTick != firstTick || segment.HostOriginNS != firstTick ||
		segment.ScaleNum != 1 || segment.ScaleDen != 1 ||
		len(segment.ObservationIDs) != 1 || segment.ObservationIDs[0] != observation.ObservationID {
		t.Fatalf("delivery affine segment = %+v", segment)
	}
}

func TestCoordinatorRejectsInvalidDeliveryObservedEvidence(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	t.Run("missing host origin", func(t *testing.T) {
		directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Start(context.Background(), deliveryObservedPlan(false, limits), testSessionID, directory, Options{
			StartProducer: fakeStarter(newFakeProducer(nil)),
		})
		if err == nil || !strings.Contains(err.Error(), "HostOrigin") {
			t.Fatalf("Start error = %v, want HostOrigin requirement", err)
		}
	})

	t.Run("producer session mapping", func(t *testing.T) {
		records := deliveryObservedRecords(t, [][]byte{{1}}, limits)
		var metadata ProducerSessionMetadata
		if err := json.Unmarshal(records[0].Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		metadata.ClockObservations = []multisensor.ClockObservation{{
			ObservationID: "forged", Tick: 0, HostBeforeNS: 0, HostAfterNS: 0,
		}}
		records[0] = record(t, sensorproducer.FrameSession, 1, metadata, nil)
		err := finishDeliveryObserved(t, records, limits)
		if err == nil || !strings.Contains(err.Error(), "must not declare clock mappings") {
			t.Fatalf("Finish error = %v, want producer mapping rejection", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*ProducerItemMetadata)
	}{
		{name: "tick", mutate: func(item *ProducerItemMetadata) { item.Tick = 1 }},
		{name: "wrap count", mutate: func(item *ProducerItemMetadata) { item.WrapCount = 1 }},
		{name: "duration", mutate: func(item *ProducerItemMetadata) { item.DurationTicks = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := deliveryObservedRecords(t, [][]byte{{1}}, limits)
			var item ProducerItemMetadata
			if err := json.Unmarshal(records[1].Metadata, &item); err != nil {
				t.Fatal(err)
			}
			test.mutate(&item)
			records[1] = record(t, sensorproducer.FrameItem, records[1].Seq, item, records[1].Payload)
			err := finishDeliveryObserved(t, records, limits)
			if err == nil || !strings.Contains(err.Error(), "tick, wrap_count, and duration_ticks must be zero") {
				t.Fatalf("Finish error = %v, want zero producer time fields", err)
			}
		})
	}
}

func TestCoordinatorItemSinkFailureFollowsSourceRequirement(t *testing.T) {
	for _, required := range []bool{true, false} {
		t.Run(map[bool]string{true: "required", false: "optional"}[required], func(t *testing.T) {
			limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
			producer := newFakeProducer(testRecords(t, [][]byte{{1, 2, 3}}, false, limits))
			wantErr := errors.New("stdout unavailable")
			failed := make(chan error, 1)
			cancelled, cancel := context.WithCancel(context.Background())
			defer cancel()
			coordinator, directory := newTestCoordinator(t, producer, required, limits, Options{
				ItemSink: itemSinkFunc(func(context.Context, multisensorstream.Item) error { return wantErr }),
				OnRequiredFailure: func(err error) {
					failed <- err
					cancel()
				},
			})
			if err := coordinator.Arm(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			err := coordinator.Finish(context.Background(), true)
			if required {
				if !errors.Is(err, wantErr) {
					t.Fatalf("required Finish error = %v", err)
				}
				select {
				case callbackErr := <-failed:
					if !errors.Is(callbackErr, wantErr) || cancelled.Err() == nil {
						t.Fatalf("required callback = %v, cancellation = %v", callbackErr, cancelled.Err())
					}
				default:
					t.Fatal("required sink failure did not cancel capture")
				}
				return
			}
			if err != nil {
				t.Fatalf("optional Finish error = %v", err)
			}
			select {
			case callbackErr := <-failed:
				t.Fatalf("optional sink failure invoked required callback: %v", callbackErr)
			default:
			}
			sources, err := coordinator.Sources()
			if err != nil {
				t.Fatal(err)
			}
			if len(sources) != 1 || sources[0].Outcome != multisensor.OutcomeFailed {
				t.Fatalf("optional source outcome = %+v", sources)
			}
			path, pathErr := directory.SourcePath("camera-0")
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("optional failed source directory remains: %v", statErr)
			}
		})
	}
}

func TestCoordinatorCancelHasNoPublishableResult(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 2, MaxPayloadBytes: 4}
	producer := newFakeProducer(testRecords(t, [][]byte{{1}}, false, limits))
	coordinator, _ := newTestCoordinator(t, producer, true, limits, Options{
		OnRequiredFailure: func(error) {},
	})
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), false); err != nil {
		t.Fatalf("Finish(false): %v", err)
	}
	if _, err := coordinator.Result(); err == nil {
		t.Fatal("Result succeeded after cancellation")
	}
	if calls := strings.Join(producer.callList(), ","); !strings.Contains(calls, "CANCEL") {
		t.Fatalf("producer calls = %s", calls)
	}
}

func TestCoordinatorRejectsProducerHashMismatch(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	producer := newFakeProducer(testRecords(t, [][]byte{{1, 2, 3}}, true, limits))
	failure := make(chan error, 1)
	coordinator, _ := newTestCoordinator(t, producer, true, limits, Options{
		OnRequiredFailure: func(err error) { failure <- err },
	})
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := coordinator.Finish(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("Finish error = %v", err)
	}
	select {
	case callbackErr := <-failure:
		if !strings.Contains(callbackErr.Error(), "required source") {
			t.Fatalf("callback error = %v", callbackErr)
		}
	case <-time.After(time.Second):
		t.Fatal("required-source failure callback was not invoked")
	}
}

func TestCoordinatorReturnsFailedOptionalSource(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 4, MaxPayloadBytes: 8}
	producer := newFakeProducer(testRecords(t, [][]byte{{1, 2, 3}}, true, limits))
	coordinator, directory := newTestCoordinator(t, producer, false, limits, Options{})
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), true); err != nil {
		t.Fatalf("Finish optional source: %v", err)
	}
	sources, err := coordinator.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].SourceID != "camera-0" ||
		sources[0].Outcome != multisensor.OutcomeFailed || len(sources[0].Artifacts) != 0 {
		t.Fatalf("optional sources = %+v", sources)
	}
	path, err := directory.SourcePath("camera-0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed optional source directory remains: %v", err)
	}
}

func TestCoordinatorBoundsItemPayload(t *testing.T) {
	limits := multisensor.SourceLimits{MaxItems: 2, MaxItemBytes: 1, MaxPayloadBytes: 4}
	producer := newFakeProducer(testRecords(t, [][]byte{{1, 2}}, false, limits))
	coordinator, _ := newTestCoordinator(t, producer, true, limits, Options{
		OnRequiredFailure: func(error) {},
	})
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Finish(context.Background(), true); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("Finish error = %v", err)
	}
}

func TestCoordinatorBoundsReadyHandshake(t *testing.T) {
	producer := newFakeProducer(nil)
	producer.readyBlock = true
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Start(context.Background(), validPlan(true, multisensor.SourceLimits{
		MaxItems: 2, MaxItemBytes: 2, MaxPayloadBytes: 4,
	}), testSessionID, directory, Options{
		ReadyTimeout: 10 * time.Millisecond, StartProducer: fakeStarter(producer),
		OnRequiredFailure: func(error) {},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start error = %v, want deadline exceeded", err)
	}
	path, pathErr := directory.SourcePath("camera-0")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed READY source remains at %s: %v", path, statErr)
	}
}

func newTestCoordinator(
	t *testing.T,
	producer *fakeProducer,
	required bool,
	limits multisensor.SourceLimits,
	options Options,
) (*Coordinator, *multisensor.Directory) {
	t.Helper()
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	options.StartProducer = fakeStarter(producer)
	coordinator, err := Start(context.Background(), validPlan(required, limits), testSessionID, directory, options)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, directory
}

func testRecords(
	t *testing.T,
	payloads [][]byte,
	badHash bool,
	limits multisensor.SourceLimits,
) []sensorproducer.Record {
	t.Helper()
	metadata := ProducerSessionMetadata{
		Schema: ProducerSessionSchema, Kind: multisensor.SourceCamera,
		Producer: multisensor.Producer{Name: "fake-camera", Version: "1.0"},
		Limits:   limits,
		Payload:  multisensor.PayloadContract{Filename: "frames.bin", Format: "camera.rgb8.v1"},
		Clock: multisensor.Clock{
			ClockID: "camera-0-clock", TickHz: 1_000_000,
			TimestampSemantics: multisensor.TimestampExposureMidpoint,
		},
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
		SyncEventSemantics: SyncEventSemanticsNone, ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
	records := []sensorproducer.Record{record(t, sensorproducer.FrameSession, 1, metadata, nil)}
	hash := sha256.New()
	var payloadBytes uint64
	for index, payload := range payloads {
		_, _ = hash.Write(payload)
		payloadBytes += uint64(len(payload))
		records = append(records, record(t, sensorproducer.FrameItem, uint64(index+2), ProducerItemMetadata{
			Schema: ProducerItemSchema, ItemIndex: uint64(index), Tick: 100 + uint64(index),
			DurationTicks: 1, SyncEventID: multisensor.NoSyncEventID,
		}, payload))
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if badHash {
		digest = strings.Repeat("0", sha256.Size*2)
	}
	endSeq := uint64(len(records) + 1)
	records = append(records,
		record(t, sensorproducer.FrameEnd, endSeq, ProducerEndMetadata{
			Schema: ProducerEndSchema, ItemCount: uint64(len(payloads)),
			PayloadBytes: payloadBytes, PayloadSHA256: digest,
		}, nil),
		record(t, sensorproducer.FrameEOF, endSeq+1, ProducerEOFMetadata{Schema: ProducerEOFSchema}, nil),
	)
	return records
}

func deliveryObservedPlan(required bool, limits multisensor.SourceLimits) Plan {
	plan := validPlan(required, limits)
	plan.Sources[0].Clock = multisensor.Clock{
		ClockID: multisensor.DeliveryObservedClockID(plan.Sources[0].SourceID),
		TickHz:  uint64(time.Second), TimestampSemantics: multisensor.TimestampDeliveryObserved,
	}
	return plan
}

func deliveryObservedRecords(
	t *testing.T,
	payloads [][]byte,
	limits multisensor.SourceLimits,
) []sensorproducer.Record {
	t.Helper()
	records := testRecords(t, payloads, false, limits)
	var session ProducerSessionMetadata
	if err := json.Unmarshal(records[0].Metadata, &session); err != nil {
		t.Fatal(err)
	}
	session.Clock = multisensor.Clock{
		ClockID: multisensor.DeliveryObservedClockID("camera-0"), TickHz: uint64(time.Second),
		TimestampSemantics: multisensor.TimestampDeliveryObserved,
	}
	session.ClockObservations = []multisensor.ClockObservation{}
	session.AffineSegments = []multisensor.AffineSegment{}
	records[0] = record(t, sensorproducer.FrameSession, records[0].Seq, session, nil)
	for recordIndex := 1; recordIndex <= len(payloads); recordIndex++ {
		var item ProducerItemMetadata
		if err := json.Unmarshal(records[recordIndex].Metadata, &item); err != nil {
			t.Fatal(err)
		}
		item.Tick = 0
		item.WrapCount = 0
		item.DurationTicks = 0
		records[recordIndex] = record(
			t, sensorproducer.FrameItem, records[recordIndex].Seq, item, records[recordIndex].Payload,
		)
	}
	return records
}

func finishDeliveryObserved(
	t *testing.T,
	records []sensorproducer.Record,
	limits multisensor.SourceLimits,
) error {
	t.Helper()
	directory, err := multisensor.CreateDirectory(filepath.Join(t.TempDir(), "aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := Start(
		context.Background(), deliveryObservedPlan(true, limits), testSessionID, directory,
		Options{
			HostOrigin: time.Now().Add(-time.Second), StartProducer: fakeStarter(newFakeProducer(records)),
			OnRequiredFailure: func(error) {},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Arm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return coordinator.Finish(context.Background(), true)
}

func record(t *testing.T, kind sensorproducer.FrameType, seq uint64, metadata any, payload []byte) sensorproducer.Record {
	t.Helper()
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return sensorproducer.Record{
		Type: kind, SessionID: testSessionID, SourceID: "camera-0", Seq: seq,
		Metadata: encoded, Payload: append([]byte(nil), payload...),
	}
}

type fakeProducer struct {
	mu         sync.Mutex
	records    chan sensorproducer.Record
	closeOnce  sync.Once
	calls      []string
	readyBlock bool
	nextErr    error
	readyErr   error
	armErr     error
	startErr   error
	stopErr    error
	cancelErr  error
	waitErr    error
	killErr    error
}

type itemSinkFunc func(context.Context, multisensorstream.Item) error

func (sink itemSinkFunc) WriteItem(ctx context.Context, item multisensorstream.Item) error {
	return sink(ctx, item)
}

type recordingItemSink struct {
	mu          sync.Mutex
	payloadPath string
	items       []multisensorstream.Item
	snapshots   [][]byte
	err         error
}

func (sink *recordingItemSink) WriteItem(_ context.Context, item multisensorstream.Item) error {
	authoritative, err := os.ReadFile(sink.payloadPath)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if err != nil {
		sink.err = err
		return err
	}
	item.Payload = append([]byte(nil), item.Payload...)
	sink.items = append(sink.items, item)
	sink.snapshots = append(sink.snapshots, authoritative)
	return nil
}

func (sink *recordingItemSink) snapshot() ([]multisensorstream.Item, [][]byte, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	items := append([]multisensorstream.Item(nil), sink.items...)
	snapshots := make([][]byte, len(sink.snapshots))
	for index := range snapshots {
		snapshots[index] = append([]byte(nil), sink.snapshots[index]...)
	}
	return items, snapshots, sink.err
}

func newFakeProducer(records []sensorproducer.Record) *fakeProducer {
	producer := &fakeProducer{records: make(chan sensorproducer.Record, len(records))}
	for _, item := range records {
		producer.records <- item
	}
	return producer
}

func fakeStarter(producer *fakeProducer) StartProducerFunc {
	return fakeStarterBySource(map[string]*fakeProducer{"camera-0": producer})
}

func fakeStarterBySource(producers map[string]*fakeProducer) StartProducerFunc {
	return func(
		_ context.Context,
		_ []string,
		_ string,
		sourceID string,
		_ sensorproducer.ProcessOptions,
	) (ProducerProcess, error) {
		producer, ok := producers[sourceID]
		if !ok {
			return nil, fmt.Errorf("unexpected source %q", sourceID)
		}
		return producer, nil
	}
}

func (producer *fakeProducer) Ready(ctx context.Context) error {
	producer.call("READY")
	if producer.readyBlock {
		<-ctx.Done()
		return ctx.Err()
	}
	return producer.readyErr
}
func (producer *fakeProducer) Arm(context.Context) error {
	producer.call("ARM")
	return producer.armErr
}
func (producer *fakeProducer) Start(context.Context) error {
	producer.call("START")
	return producer.startErr
}
func (producer *fakeProducer) Stop(context.Context) error {
	producer.call("STOP")
	producer.close()
	return producer.stopErr
}
func (producer *fakeProducer) Cancel(context.Context) error {
	producer.call("CANCEL")
	producer.close()
	return producer.cancelErr
}
func (producer *fakeProducer) Next(ctx context.Context) (sensorproducer.Record, error) {
	if producer.nextErr != nil {
		return sensorproducer.Record{}, producer.nextErr
	}
	select {
	case item, ok := <-producer.records:
		if !ok {
			return sensorproducer.Record{}, io.EOF
		}
		return item, nil
	case <-ctx.Done():
		return sensorproducer.Record{}, ctx.Err()
	}
}
func (producer *fakeProducer) Wait(context.Context) error {
	producer.call("WAIT")
	return producer.waitErr
}
func (producer *fakeProducer) Kill() error {
	producer.call("KILL")
	producer.close()
	return producer.killErr
}

func (producer *fakeProducer) call(name string) {
	producer.mu.Lock()
	defer producer.mu.Unlock()
	producer.calls = append(producer.calls, name)
}

func (producer *fakeProducer) callList() []string {
	producer.mu.Lock()
	defer producer.mu.Unlock()
	return append([]string(nil), producer.calls...)
}

func (producer *fakeProducer) close() {
	producer.closeOnce.Do(func() { close(producer.records) })
}

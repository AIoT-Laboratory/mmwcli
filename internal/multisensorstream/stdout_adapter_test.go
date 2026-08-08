package multisensorstream

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStdoutAdapterTerminalRecordsAreFollowedByPhysicalEOF(t *testing.T) {
	tests := []struct {
		name     string
		terminal RecordType
		finish   func(*StdoutAdapter) error
	}{
		{
			name: "commit", terminal: RecordCommit,
			finish: func(adapter *StdoutAdapter) error {
				if err := adapter.EndSource(context.Background(), "radar-0", OutcomeComplete); err != nil {
					return err
				}
				if err := adapter.EndSource(context.Background(), "camera-0", OutcomeOmitted); err != nil {
					return err
				}
				return adapter.Commit(context.Background(), adapterTestArtifact())
			},
		},
		{
			name: "abort", terminal: RecordAbort,
			finish: func(adapter *StdoutAdapter) error {
				return adapter.Abort(context.Background(), AbortCancelled)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, readResult := newPipeAdapter(t, adapterTestSession())
			if err := test.finish(adapter); err != nil {
				t.Fatal(err)
			}
			result := awaitAdapterRead(t, readResult)
			if result.err != nil {
				t.Fatal(result.err)
			}
			if len(result.records) < 2 ||
				result.records[len(result.records)-2].Type != test.terminal ||
				result.records[len(result.records)-1].Type != RecordEOF {
				t.Fatalf("terminal records = %v", adapterRecordTypes(result.records))
			}
		})
	}
}

func TestStdoutAdapterCancellationInterruptsBlockedOSPipeWrite(t *testing.T) {
	session := adapterTestSession()
	session.Sources = session.Sources[:1]
	session.Sources[0].Limits = SourceLimits{
		MaxItems: 1, MaxItemBytes: 8 << 20, MaxPayloadBytes: 8 << 20,
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	adapter, err := NewStdoutAdapter(context.Background(), writer, session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if record, err := readRecord(reader); err != nil || record.Type != RecordSession {
		t.Fatalf("initial SESSION = %+v, %v", record, err)
	}
	writeAdapterRadarStart(t, adapter)

	ctx, cancel := context.WithCancel(context.Background())
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- adapter.WriteItem(ctx, Item{
			SourceID: "radar-0", ItemIndex: 0, DurationTicks: 1,
			SyncEventID: NoSyncEventID, Payload: make([]byte, 4<<20),
		})
	}()
	select {
	case err := <-writeResult:
		t.Fatalf("large pipe ITEM did not block: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := awaitAdapterError(t, writeResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ITEM error = %v", err)
	}

	drainResult := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		drainResult <- err
	}()
	if err := awaitAdapterError(t, drainResult); err != nil {
		t.Fatalf("drain canceled stdout to physical EOF: %v", err)
	}
}

func TestStdoutAdapterSerializesCameraAndRadarGoroutines(t *testing.T) {
	adapter, readResult := newPipeAdapter(t, adapterTestSession())
	radarSink, err := NewRadarSink(adapter, "radar-0", 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	writeAdapterRadarStart(t, adapter)
	if err := radarSink.ReleaseRadarStart(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsBySource := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		for index, payload := range [][]byte{[]byte("R0"), []byte("R1")} {
			if err := radarSink.WriteFrame(context.Background(), uint64(index), payload); err != nil {
				errorsBySource <- err
				return
			}
		}
		errorsBySource <- nil
	}()
	go func() {
		defer group.Done()
		<-start
		for index, payload := range [][]byte{[]byte("C0"), []byte("C1")} {
			if err := adapter.WriteItem(context.Background(), Item{
				SourceID: "camera-0", ItemIndex: uint64(index), Tick: uint64(index * 5),
				DurationTicks: 2, SyncEventID: NoSyncEventID, Payload: payload,
			}); err != nil {
				errorsBySource <- err
				return
			}
		}
		errorsBySource <- nil
	}()
	close(start)
	group.Wait()
	for range 2 {
		if err := <-errorsBySource; err != nil {
			t.Fatal(err)
		}
	}

	endErrors := make(chan error, 2)
	go func() {
		endErrors <- adapter.EndSource(context.Background(), "radar-0", OutcomeComplete)
	}()
	go func() {
		endErrors <- adapter.EndSource(context.Background(), "camera-0", OutcomeComplete)
	}()
	for range 2 {
		if err := <-endErrors; err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.Commit(context.Background(), adapterTestArtifact()); err != nil {
		t.Fatal(err)
	}
	result := awaitAdapterRead(t, readResult)
	if result.err != nil {
		t.Fatal(result.err)
	}

	nextItem := map[string]uint64{"radar-0": 0, "camera-0": 0}
	for _, record := range result.records {
		if record.Type != RecordItem {
			continue
		}
		var item itemRecordV1
		if err := decodeExactMetadata(
			record.Metadata,
			&item,
			"schema", "source_id", "item_index", "provisional", "tick", "wrap_count", "duration_ticks", "sync_event_id",
		); err != nil {
			t.Fatal(err)
		}
		if item.ItemIndex != nextItem[item.SourceID] {
			t.Fatalf("source %q item_index = %d, want %d", item.SourceID, item.ItemIndex, nextItem[item.SourceID])
		}
		nextItem[item.SourceID]++
		if item.SourceID == "radar-0" {
			wantTick := item.ItemIndex * uint64(10*time.Millisecond)
			if item.Tick != wantTick || item.DurationTicks != uint64(10*time.Millisecond) ||
				item.WrapCount != 0 || item.SyncEventID != NoSyncEventID {
				t.Fatalf("radar ITEM geometry = %+v", item)
			}
		}
	}
	if nextItem["radar-0"] != 2 || nextItem["camera-0"] != 2 {
		t.Fatalf("interleaved ITEM totals = %v", nextItem)
	}
}

func TestRadarSinkWaitsForRadarStartAndCancellationDoesNotLeak(t *testing.T) {
	session := adapterTestSession()
	session.Sources = session.Sources[:1]
	adapter, readResult := newPipeAdapter(t, session)
	radarSink, err := NewRadarSink(adapter, "radar-0", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- radarSink.WriteFrame(ctx, 0, []byte("radar"))
	}()
	select {
	case err := <-writeResult:
		t.Fatalf("radar frame did not wait for RADAR_START: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := awaitAdapterError(t, writeResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled radar start wait = %v", err)
	}
	if err := adapter.Abort(context.Background(), AbortCancelled); err != nil {
		t.Fatal(err)
	}
	if result := awaitAdapterRead(t, readResult); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestRadarSinkRejectsFramePeriodOverflow(t *testing.T) {
	session := adapterTestSession()
	session.Sources = session.Sources[:1]
	session.Sources[0].Limits.MaxItems = 4
	adapter, readResult := newPipeAdapter(t, session)
	if _, err := NewRadarSink(adapter, "radar-0", time.Duration(math.MaxInt64)); err == nil ||
		!strings.Contains(err.Error(), "overflows") {
		t.Fatalf("RadarSink overflow error = %v", err)
	}
	if err := adapter.Abort(context.Background(), AbortIntegrityFailed); err != nil {
		t.Fatal(err)
	}
	if result := awaitAdapterRead(t, readResult); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestStdoutAdapterPoisonClosesWithoutTerminalAppend(t *testing.T) {
	adapter, readResult := newPipeAdapter(t, adapterTestSession())
	writeAdapterRadarStart(t, adapter)
	err := adapter.WriteItem(context.Background(), Item{
		SourceID: "radar-0", ItemIndex: 1, Payload: []byte("out-of-order"),
	})
	if err == nil || !strings.Contains(err.Error(), "expected 0") {
		t.Fatalf("poison error = %v", err)
	}
	result := awaitAdapterRead(t, readResult)
	if !errors.Is(result.err, ErrProtocol) || len(result.records) != 2 ||
		result.records[0].Type != RecordSession || result.records[1].Type != RecordRadarStart {
		t.Fatalf("poisoned stream result = %v records %v", result.err, adapterRecordTypes(result.records))
	}
}

func writeAdapterRadarStart(t *testing.T, adapter *StdoutAdapter) {
	t.Helper()
	if err := adapter.WriteRadarStart(context.Background(), RadarStart{
		SourceID: "radar-0", HostLowerNS: 1_000_000_000, HostUpperNS: 1_000_000_100,
	}); err != nil {
		t.Fatal(err)
	}
}

type adapterReadResult struct {
	records []Record
	err     error
}

func newPipeAdapter(
	t *testing.T,
	session Session,
) (*StdoutAdapter, <-chan adapterReadResult) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan adapterReadResult, 1)
	go func() {
		decoder, err := NewDecoder(reader)
		if err != nil {
			result <- adapterReadResult{err: err}
			return
		}
		var records []Record
		for {
			record, err := decoder.Read()
			if errors.Is(err, io.EOF) {
				result <- adapterReadResult{records: records}
				return
			}
			if err != nil {
				result <- adapterReadResult{records: records, err: err}
				return
			}
			records = append(records, record)
		}
	}()
	adapter, err := NewStdoutAdapter(context.Background(), writer, session)
	if err != nil {
		_ = reader.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = adapter.Close()
		_ = reader.Close()
	})
	return adapter, result
}

func adapterTestSession() Session {
	session := testSession()
	session.Sources[0].Clock.TickHz = uint64(time.Second)
	return session
}

func adapterTestArtifact() SessionArtifact {
	payload := []byte(`{"schema":"mmwcli.multisensor_session.v1"}`)
	return SessionArtifact{SizeBytes: uint64(len(payload)), SHA256: sha256.Sum256(payload)}
}

func awaitAdapterRead(t *testing.T, result <-chan adapterReadResult) adapterReadResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for multisensor stream pipe EOF")
		return adapterReadResult{}
	}
}

func awaitAdapterError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for multisensor stream operation")
		return nil
	}
}

func adapterRecordTypes(records []Record) []RecordType {
	types := make([]RecordType, len(records))
	for index, record := range records {
		types[index] = record.Type
	}
	return types
}

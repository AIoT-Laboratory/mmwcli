package multisensorstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCommittedStreamMatchesDeterministicGoldenAndInterleavesSources(t *testing.T) {
	stream := encodeCommittedStream(t)
	want, err := os.ReadFile("testdata/two-source-stream.hex")
	if err != nil {
		t.Fatal(err)
	}
	actualHex := formatHex(stream)
	if strings.TrimSpace(string(want)) != actualHex {
		t.Fatalf("stream does not match deterministic golden; actual:\n%s", actualHex)
	}

	if string(stream[:8]) != "MMWMSTR1" || binary.LittleEndian.Uint16(stream[8:10]) != ProtocolMajor ||
		binary.LittleEndian.Uint16(stream[10:12]) != RecordHeaderSize {
		t.Fatalf("first little-endian header is invalid: %x", stream[:16])
	}

	decoder, err := NewDecoder(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var records []Record
	for {
		record, err := decoder.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	wantTypes := []RecordType{
		RecordSession, RecordRadarConfig,
		RecordItem, RecordRadarStart, RecordItem, RecordItem, RecordItem,
		RecordEnd, RecordEnd, RecordCommit, RecordEOF,
	}
	if len(records) != len(wantTypes) {
		t.Fatalf("record count = %d, want %d", len(records), len(wantTypes))
	}
	for index, record := range records {
		if record.Type != wantTypes[index] || record.RecordSeq != uint64(index) {
			t.Fatalf("record %d = type %d seq %d", index, record.Type, record.RecordSeq)
		}
	}

	wantItems := []struct {
		source string
		index  uint64
		data   string
	}{
		{"camera-0", 0, "JPEG-A"},
		{"radar-0", 0, "R000"},
		{"radar-0", 1, "R1"},
		{"camera-0", 1, "JPEG-B"},
	}
	itemRecords := []Record{records[2], records[4], records[5], records[6]}
	for itemIndex, wantItem := range wantItems {
		record := itemRecords[itemIndex]
		var metadata itemRecordV1
		if err := decodeExactMetadata(
			record.Metadata,
			&metadata,
			"schema", "source_id", "item_index", "provisional", "tick", "wrap_count", "duration_ticks", "sync_event_id",
		); err != nil {
			t.Fatal(err)
		}
		if metadata.SourceID != wantItem.source || metadata.ItemIndex != wantItem.index ||
			!metadata.Provisional || string(record.Payload) != wantItem.data {
			t.Fatalf("ITEM %d = %+v payload %q", itemIndex, metadata, record.Payload)
		}
	}
	var radarStart radarStartRecordV1
	if err := decodeExactMetadata(
		records[3].Metadata,
		&radarStart,
		"schema", "source_id", "host_lower_ns", "host_upper_ns",
	); err != nil {
		t.Fatal(err)
	}
	if radarStart.Schema != RadarStartSchemaV1 || radarStart.SourceID != "radar-0" ||
		radarStart.HostLowerNS != 1_000_000_000 || radarStart.HostUpperNS != 1_000_000_100 {
		t.Fatalf("RADAR_START = %+v", radarStart)
	}

	var cameraEnd endRecordV1
	if err := decodeExactMetadata(
		records[7].Metadata,
		&cameraEnd,
		"schema", "source_id", "outcome", "item_count", "payload_bytes", "payload_sha256",
	); err != nil {
		t.Fatal(err)
	}
	if cameraEnd.SourceID != "camera-0" || cameraEnd.Outcome != OutcomeFailed ||
		cameraEnd.ItemCount != 2 || cameraEnd.PayloadBytes != 12 {
		t.Fatalf("camera END = %+v", cameraEnd)
	}
	// Camera ITEMs remain visible as provisional records, but its failed END
	// tells a committed-stream consumer to discard both of them.
	kept := 0
	for _, record := range itemRecords {
		var item itemRecordV1
		if err := json.Unmarshal(record.Metadata, &item); err != nil {
			t.Fatal(err)
		}
		if item.SourceID == "radar-0" {
			kept++
		}
	}
	if kept != 2 {
		t.Fatalf("complete-source retained ITEMs = %d, want 2", kept)
	}
}

func TestDecoderRejectsCorruptionSequenceAndPerSourceOrder(t *testing.T) {
	original := encodeCommittedStream(t)
	tests := []struct {
		name   string
		match  string
		mutate func(*testing.T, []byte)
	}{
		{
			name:  "payload corruption",
			match: "digest mismatch",
			mutate: func(t *testing.T, stream []byte) {
				span := recordSpans(t, stream)[2]
				stream[span.payloadStart] ^= 0xff
			},
		},
		{
			name:  "record sequence gap",
			match: "record_seq",
			mutate: func(t *testing.T, stream []byte) {
				span := recordSpans(t, stream)[3]
				binary.LittleEndian.PutUint64(stream[span.start+16:span.start+24], 99)
				recomputeRecordDigest(t, stream, span)
			},
		},
		{
			name:  "radar item gap despite camera interleave",
			match: "item_index",
			mutate: func(t *testing.T, stream []byte) {
				span := recordSpans(t, stream)[5]
				metadata := stream[span.metadataStart:span.payloadStart]
				field := []byte(`"item_index":1`)
				position := bytes.Index(metadata, field)
				if position < 0 {
					t.Fatalf("item_index field not found in %s", metadata)
				}
				metadata[position+len(field)-1] = '2'
				recomputeRecordDigest(t, stream, span)
			},
		},
		{
			name:  "reversed radar start bounds",
			match: "bounds are reversed",
			mutate: func(t *testing.T, stream []byte) {
				span := recordSpans(t, stream)[3]
				metadata := stream[span.metadataStart:span.payloadStart]
				field := []byte(`"host_lower_ns":1000000000`)
				position := bytes.Index(metadata, field)
				if position < 0 {
					t.Fatalf("host_lower_ns field not found in %s", metadata)
				}
				copy(metadata[position:position+len(field)], []byte(`"host_lower_ns":1000000200`))
				recomputeRecordDigest(t, stream, span)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := append([]byte(nil), original...)
			test.mutate(t, stream)
			err := firstDecodeError(t, stream)
			if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("decode error = %v, want protocol error containing %q", err, test.match)
			}
		})
	}
}

func TestRadarStartAllowsCameraFirstAndRejectsInvalidState(t *testing.T) {
	var output bytes.Buffer
	encoder, err := NewEncoder(&output, testSession())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteItem(Item{
		SourceID: "camera-0", ItemIndex: 0, Tick: 10, DurationTicks: 2,
		SyncEventID: NoSyncEventID, Payload: []byte("camera-first"),
	}); err != nil {
		t.Fatalf("camera ITEM before RADAR_START: %v", err)
	}
	start := RadarStart{SourceID: "radar-0", HostLowerNS: 100, HostUpperNS: 200}
	if err := encoder.WriteRadarStart(start); err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteItem(Item{
		SourceID: "radar-0", ItemIndex: 0, Tick: 100, DurationTicks: 10,
		SyncEventID: NoSyncEventID, Payload: []byte("radar"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteRadarStart(start); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate RADAR_START error = %v", err)
	}

	tests := []struct {
		name  string
		apply func(*Encoder) error
		match string
	}{
		{
			name: "camera source",
			apply: func(encoder *Encoder) error {
				return encoder.WriteRadarStart(RadarStart{SourceID: "camera-0"})
			},
			match: "not a declared radar",
		},
		{
			name: "reversed bounds",
			apply: func(encoder *Encoder) error {
				return encoder.WriteRadarStart(RadarStart{
					SourceID: "radar-0", HostLowerNS: 2, HostUpperNS: 1,
				})
			},
			match: "must not exceed",
		},
		{
			name: "radar item before start",
			apply: func(encoder *Encoder) error {
				return encoder.WriteItem(Item{
					SourceID: "radar-0", ItemIndex: 0, Tick: 100, DurationTicks: 10,
					SyncEventID: NoSyncEventID, Payload: []byte("radar"),
				})
			},
			match: "requires a preceding RADAR_START",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			encoder, err := NewEncoder(&output, testSession())
			if err != nil {
				t.Fatal(err)
			}
			before := output.Len()
			if err := test.apply(encoder); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v, want %q", err, test.match)
			}
			if output.Len() != before {
				t.Fatal("invalid RADAR_START state changed stdout")
			}
		})
	}
}

func TestDecoderRejectsRadarStartSourceOrderUniquenessAndExactMetadata(t *testing.T) {
	sessionMetadata, err := encodeMetadata(sessionRecordV1{
		Schema: SchemaV1, SessionID: testSession().SessionID,
		SynchronizationGrade: testSession().SynchronizationGrade, Sources: testSession().Sources,
	})
	if err != nil {
		t.Fatal(err)
	}
	startMetadata, err := encodeMetadata(radarStartRecordV1{
		Schema: RadarStartSchemaV1, SourceID: "radar-0", HostLowerNS: 100, HostUpperNS: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	cameraStartMetadata, err := encodeMetadata(radarStartRecordV1{
		Schema: RadarStartSchemaV1, SourceID: "camera-0", HostLowerNS: 100, HostUpperNS: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	itemMetadata, err := encodeMetadata(itemRecordV1{
		Schema: ItemSchemaV1, SourceID: "radar-0", ItemIndex: 0, Provisional: true,
		Tick: 100, DurationTicks: 10, SyncEventID: NoSyncEventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		records []Record
		match   string
	}{
		{
			name: "radar item before start",
			records: []Record{
				{Type: RecordItem, Metadata: itemMetadata, Payload: []byte("radar")},
			},
			match: "preceding RADAR_START",
		},
		{
			name: "camera source",
			records: []Record{
				{Type: RecordRadarStart, Metadata: cameraStartMetadata},
			},
			match: "source contract",
		},
		{
			name: "duplicate",
			records: []Record{
				{Type: RecordRadarStart, Metadata: startMetadata},
				{Type: RecordRadarStart, Metadata: startMetadata},
			},
			match: "unique",
		},
		{
			name: "after radar item",
			records: []Record{
				{Type: RecordRadarStart, Metadata: startMetadata},
				{Type: RecordItem, Metadata: itemMetadata, Payload: []byte("radar")},
				{Type: RecordRadarStart, Metadata: startMetadata},
			},
			match: "unique",
		},
		{
			name: "extra metadata",
			records: []Record{
				{
					Type:     RecordRadarStart,
					Metadata: []byte(`{"schema":"mmwcli.multisensor_stream_radar_start.v1","source_id":"radar-0","host_lower_ns":100,"host_upper_ns":200,"extra":true}`),
				},
			},
			match: "exact key set",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			records := append([]Record{{Type: RecordSession, Metadata: sessionMetadata}}, test.records...)
			for index, record := range records {
				record.RecordSeq = uint64(index)
				if err := writeRecord(&output, record); err != nil {
					t.Fatal(err)
				}
			}
			if err := firstDecodeError(t, output.Bytes()); !errors.Is(err, ErrProtocol) ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("decode error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestEncoderPoisonsStateViolationsAndShortWrites(t *testing.T) {
	var output bytes.Buffer
	encoder, err := NewEncoder(&output, testSession())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteRadarStart(RadarStart{SourceID: "radar-0"}); err != nil {
		t.Fatal(err)
	}
	before := output.Len()
	err = encoder.WriteItem(Item{SourceID: "radar-0", ItemIndex: 1, Payload: []byte("bad")})
	if err == nil || !strings.Contains(err.Error(), "expected 0") {
		t.Fatalf("out-of-order ITEM error = %v", err)
	}
	if output.Len() != before {
		t.Fatal("invalid ITEM changed stdout")
	}
	if err := encoder.Abort(AbortCancelled); !errors.Is(err, ErrEncoderPoisoned) {
		t.Fatalf("poisoned Abort error = %v", err)
	}

	short := &shortCallWriter{shortCall: 5}
	encoder, err = NewEncoder(short, testSession())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteRadarStart(RadarStart{SourceID: "radar-0"}); err != nil {
		t.Fatal(err)
	}
	err = encoder.WriteItem(Item{
		SourceID: "radar-0", ItemIndex: 0, Tick: 1, DurationTicks: 1,
		SyncEventID: NoSyncEventID, Payload: []byte("raw"),
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short-write error = %v", err)
	}
	calls := short.calls
	if err := encoder.EndSource("radar-0", OutcomeComplete); !errors.Is(err, ErrEncoderPoisoned) {
		t.Fatalf("poisoned END error = %v", err)
	}
	if short.calls != calls {
		t.Fatal("poisoned encoder retried its writer")
	}

	output.Reset()
	encoder, err = NewEncoder(&output, testSession())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.EndSource("radar-0", OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	err = encoder.WriteItem(Item{
		SourceID: "camera-0", ItemIndex: 0, Tick: 1, DurationTicks: 1,
		SyncEventID: NoSyncEventID, Payload: []byte("late"),
	})
	if err == nil || !strings.Contains(err.Error(), "first source END") {
		t.Fatalf("ITEM after END error = %v", err)
	}
}

func TestCommitForbidsAppendAndDecoderRejectsTrailingRecord(t *testing.T) {
	var output bytes.Buffer
	encoder := writeCommittedStream(t, &output)
	committedBytes := output.Len()
	err := encoder.WriteItem(Item{SourceID: "radar-0", ItemIndex: 2, Payload: []byte("late")})
	if !errors.Is(err, ErrEncoderTerminal) || output.Len() != committedBytes {
		t.Fatalf("post-COMMIT append = %v, bytes = %d", err, output.Len())
	}

	lateMetadata, err := encodeMetadata(itemRecordV1{
		Schema: ItemSchemaV1, SourceID: "radar-0", ItemIndex: 2, Provisional: true,
		Tick: 120, DurationTicks: 10, SyncEventID: NoSyncEventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(&output, Record{
		Type: RecordItem, RecordSeq: 11, Metadata: lateMetadata, Payload: []byte("late"),
	}); err != nil {
		t.Fatal(err)
	}
	decoder, err := NewDecoder(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 11; index++ {
		if _, err := decoder.Read(); err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
	}
	if _, err := decoder.Read(); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "ITEM") {
		t.Fatalf("trailing ITEM error = %v", err)
	}
}

func TestCommitRequiresEveryRequiredSourceToBeComplete(t *testing.T) {
	session := testSession()
	session.Sources[1].Required = true
	var output bytes.Buffer
	encoder, err := NewEncoder(&output, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.EndSource("camera-0", OutcomeFailed); err != nil {
		t.Fatal(err)
	}
	if err := encoder.EndSource("radar-0", OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	artifactBytes := []byte(`{"schema":"mmwcli.multisensor_session.v1"}`)
	artifact := SessionArtifact{
		SizeBytes: uint64(len(artifactBytes)), SHA256: sha256.Sum256(artifactBytes),
	}
	if err := encoder.Commit(artifact); err == nil || !strings.Contains(err.Error(), "required source") {
		t.Fatalf("required failed-source COMMIT error = %v", err)
	}

	output.Reset()
	sessionMetadata, _ := encodeMetadata(sessionRecordV1{
		Schema: SchemaV1, SessionID: session.SessionID,
		SynchronizationGrade: session.SynchronizationGrade, Sources: session.Sources,
	})
	emptyDigest := sha256.Sum256(nil)
	cameraEnd, _ := encodeMetadata(endRecordV1{
		Schema: EndSchemaV1, SourceID: "camera-0", Outcome: OutcomeFailed,
		PayloadSHA256: digestString(emptyDigest),
	})
	radarEnd, _ := encodeMetadata(endRecordV1{
		Schema: EndSchemaV1, SourceID: "radar-0", Outcome: OutcomeComplete,
		PayloadSHA256: digestString(emptyDigest),
	})
	commit, _ := encodeMetadata(commitRecordV1{
		Schema: TerminalSchemaV1, SessionID: session.SessionID, Outcome: "commit",
		SessionJSONBytes: artifact.SizeBytes, SessionJSONSHA256: digestString(artifact.SHA256),
	})
	for _, record := range []Record{
		{Type: RecordSession, RecordSeq: 0, Metadata: sessionMetadata},
		{Type: RecordEnd, RecordSeq: 1, Metadata: cameraEnd},
		{Type: RecordEnd, RecordSeq: 2, Metadata: radarEnd},
		{Type: RecordCommit, RecordSeq: 3, Metadata: commit},
	} {
		if err := writeRecord(&output, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := firstDecodeError(t, output.Bytes()); !errors.Is(err, ErrProtocol) ||
		!strings.Contains(err.Error(), "required source") {
		t.Fatalf("decoder required failed-source COMMIT error = %v", err)
	}
}

func TestAbortReasonVocabulary(t *testing.T) {
	reasons := []AbortReason{
		AbortCancelled,
		AbortBackpressure,
		AbortSourceFailed,
		AbortIntegrityFailed,
		AbortCleanupFailed,
		AbortPublishFailed,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			var output bytes.Buffer
			encoder, err := NewEncoder(&output, testSession())
			if err != nil {
				t.Fatal(err)
			}
			if err := encoder.Abort(reason); err != nil {
				t.Fatal(err)
			}
			decoder, err := NewDecoder(bytes.NewReader(output.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decoder.Read(); err != nil {
				t.Fatal(err)
			}
			record, err := decoder.Read()
			if err != nil {
				t.Fatal(err)
			}
			var terminal abortRecordV1
			if err := decodeExactMetadata(
				record.Metadata,
				&terminal,
				"schema", "session_id", "outcome", "reason_code",
			); err != nil {
				t.Fatal(err)
			}
			if terminal.ReasonCode != reason {
				t.Fatalf("reason = %q, want %q", terminal.ReasonCode, reason)
			}
			if record, err := decoder.Read(); err != nil || record.Type != RecordEOF {
				t.Fatalf("EOF record = %+v, %v", record, err)
			}
			if _, err := decoder.Read(); !errors.Is(err, io.EOF) {
				t.Fatalf("transport EOF = %v", err)
			}
		})
	}
}

func encodeCommittedStream(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	writeCommittedStream(t, &output)
	return output.Bytes()
}

func writeCommittedStream(t *testing.T, output *bytes.Buffer) *Encoder {
	t.Helper()
	encoder, err := NewEncoder(output, testSession())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteRadarConfig("radar-0", "ti.mmwave_cli.cfg.v1", []byte("sensorStop\n")); err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteItem(Item{
		SourceID: "camera-0", ItemIndex: 0, Tick: 10, DurationTicks: 2,
		SyncEventID: 7, Payload: []byte("JPEG-A"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteRadarStart(RadarStart{
		SourceID: "radar-0", HostLowerNS: 1_000_000_000, HostUpperNS: 1_000_000_100,
	}); err != nil {
		t.Fatal(err)
	}
	items := []Item{
		{SourceID: "radar-0", ItemIndex: 0, Tick: 100, DurationTicks: 10, SyncEventID: 7, Payload: []byte("R000")},
		{SourceID: "radar-0", ItemIndex: 1, Tick: 110, DurationTicks: 10, SyncEventID: NoSyncEventID, Payload: []byte("R1")},
		{SourceID: "camera-0", ItemIndex: 1, Tick: 20, DurationTicks: 2, SyncEventID: NoSyncEventID, Payload: []byte("JPEG-B")},
	}
	for _, item := range items {
		if err := encoder.WriteItem(item); err != nil {
			t.Fatal(err)
		}
	}
	if err := encoder.EndSource("camera-0", OutcomeFailed); err != nil {
		t.Fatal(err)
	}
	if err := encoder.EndSource("radar-0", OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	sessionJSON := []byte(`{"schema":"mmwcli.multisensor_session.v1"}`)
	artifact := SessionArtifact{SizeBytes: uint64(len(sessionJSON)), SHA256: sha256.Sum256(sessionJSON)}
	if err := encoder.Commit(artifact); err != nil {
		t.Fatal(err)
	}
	return encoder
}

func testSession() Session {
	return Session{
		SessionID:            "123e4567-e89b-42d3-a456-426614174000",
		SynchronizationGrade: SynchronizationSoftwareBarrier,
		Sources: []Source{
			{
				SourceID: "radar-0", Kind: SourceRadar, Required: true,
				Payload: PayloadContract{Filename: "adc.bin", Format: "ti.raw_adc.v1"},
				Clock:   Clock{ClockID: "radar-clock", TickHz: 1_000_000, TimestampSemantics: TimestampFrameStart},
				Limits:  SourceLimits{MaxItems: 2, MaxItemBytes: 16, MaxPayloadBytes: 32},
			},
			{
				SourceID: "camera-0", Kind: SourceCamera, Required: false,
				Payload: PayloadContract{Filename: "frames.bin", Format: "image.jpeg.v1"},
				Clock:   Clock{ClockID: "camera-clock", TickHz: 1_000, TimestampSemantics: TimestampExposureMidpoint},
				Limits:  SourceLimits{MaxItems: 2, MaxItemBytes: 16, MaxPayloadBytes: 32},
			},
		},
	}
}

type recordSpan struct {
	start         int
	metadataStart int
	payloadStart  int
	end           int
}

func recordSpans(t *testing.T, stream []byte) []recordSpan {
	t.Helper()
	var spans []recordSpan
	for offset := 0; offset < len(stream); {
		if len(stream)-offset < RecordHeaderSize {
			t.Fatalf("truncated test stream at byte %d", offset)
		}
		metadataBytes := binary.LittleEndian.Uint64(stream[offset+24 : offset+32])
		payloadBytes := binary.LittleEndian.Uint64(stream[offset+32 : offset+40])
		end := uint64(offset+RecordHeaderSize) + metadataBytes + payloadBytes
		if end > uint64(len(stream)) {
			t.Fatalf("record at %d exceeds test stream", offset)
		}
		span := recordSpan{
			start: offset, metadataStart: offset + RecordHeaderSize,
			payloadStart: offset + RecordHeaderSize + int(metadataBytes), end: int(end),
		}
		spans = append(spans, span)
		offset = span.end
	}
	return spans
}

func recomputeRecordDigest(t *testing.T, stream []byte, span recordSpan) {
	t.Helper()
	header := stream[span.start : span.start+RecordHeaderSize]
	digest := recordDigest(
		header[:48],
		stream[span.metadataStart:span.payloadStart],
		stream[span.payloadStart:span.end],
	)
	copy(header[48:80], digest[:])
}

func firstDecodeError(t *testing.T, stream []byte) error {
	t.Helper()
	decoder, err := NewDecoder(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	for reads := 0; reads < 32; reads++ {
		if _, err := decoder.Read(); err != nil {
			return err
		}
	}
	t.Fatal("decoder did not terminate")
	return nil
}

func formatHex(payload []byte) string {
	encoded := hex.EncodeToString(payload)
	lines := make([]string, 0, (len(encoded)+95)/96)
	for len(encoded) > 96 {
		lines = append(lines, encoded[:96])
		encoded = encoded[96:]
	}
	if encoded != "" {
		lines = append(lines, encoded)
	}
	return strings.Join(lines, "\n")
}

type shortCallWriter struct {
	bytes.Buffer
	calls     int
	shortCall int
}

func (writer *shortCallWriter) Write(payload []byte) (int, error) {
	writer.calls++
	if writer.calls == writer.shortCall {
		written := len(payload) - 1
		_, _ = writer.Buffer.Write(payload[:written])
		return written, nil
	}
	return writer.Buffer.Write(payload)
}

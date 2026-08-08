package multisensor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const multisensorGoldenSchema = "mmwcli.multisensor_golden.v1"

type crossLanguageGolden struct {
	Schema      string               `json:"schema"`
	SessionJSON string               `json:"session_json"`
	Sources     []goldenSourceVector `json:"sources"`
}

type goldenSourceVector struct {
	SourceID   string `json:"source_id"`
	PayloadHex string `json:"payload_hex"`
	IndexHex   string `json:"index_hex"`
}

func TestTwoSourceCrossLanguageGolden(t *testing.T) {
	session, indexes, payloads := twoSourceFixture(t)
	sessionJSON, err := MarshalSession(session)
	if err != nil {
		t.Fatal(err)
	}
	golden := crossLanguageGolden{Schema: multisensorGoldenSchema, SessionJSON: string(sessionJSON)}
	for _, source := range session.Sources {
		indexBytes, err := EncodeSensorIndex(indexes[source.SourceID], source.Limits)
		if err != nil {
			t.Fatal(err)
		}
		golden.Sources = append(golden.Sources, goldenSourceVector{
			SourceID:   source.SourceID,
			PayloadHex: hex.EncodeToString(payloads[source.SourceID]),
			IndexHex:   hex.EncodeToString(indexBytes),
		})
	}
	want, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("testdata", "two-source-golden.json")
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v\nexpected contents:\n%s", err, want)
	}
	if !bytes.Equal(actual, want) {
		t.Fatalf("cross-language golden differs\nexpected contents:\n%s", want)
	}

	var decodedGolden crossLanguageGolden
	if err := json.Unmarshal(actual, &decodedGolden); err != nil {
		t.Fatal(err)
	}
	decodedSession, err := UnmarshalSession([]byte(decodedGolden.SessionJSON))
	if err != nil {
		t.Fatal(err)
	}
	decodedIndexes := make(map[string]SensorIndex, len(decodedGolden.Sources))
	for _, vector := range decodedGolden.Sources {
		source := sourceByID(t, decodedSession, vector.SourceID)
		payload, err := hex.DecodeString(vector.PayloadHex)
		if err != nil {
			t.Fatal(err)
		}
		payloadArtifact := artifactForRole(t, source, ArtifactPayload)
		digest := sha256.Sum256(payload)
		if uint64(len(payload)) != payloadArtifact.SizeBytes ||
			hex.EncodeToString(digest[:]) != payloadArtifact.SHA256 {
			t.Fatalf("golden payload %q does not match its artifact", vector.SourceID)
		}
		indexBytes, err := hex.DecodeString(vector.IndexHex)
		if err != nil {
			t.Fatal(err)
		}
		decodedIndexes[vector.SourceID], err = DecodeSensorIndex(indexBytes, source.Limits)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := decodedSession.ValidateWithIndexes(decodedIndexes); err != nil {
		t.Fatal(err)
	}
}

func TestSessionJSONIsClosedAndDeterministic(t *testing.T) {
	session, indexes, _ := twoSourceFixture(t)
	encoded, err := MarshalSession(session)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalSession(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, session) {
		t.Fatalf("decoded session differs:\n got %+v\nwant %+v", decoded, session)
	}
	if err := decoded.ValidateWithIndexes(indexes); err != nil {
		t.Fatal(err)
	}
	reencoded, err := MarshalSession(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatal("session JSON encoding is not deterministic")
	}

	for _, test := range []struct {
		name    string
		encoded []byte
		match   string
	}{
		{
			name:    "unknown field",
			encoded: bytes.Replace(encoded, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1),
			match:   "unknown field",
		},
		{
			name: "duplicate field",
			encoded: bytes.Replace(
				encoded,
				[]byte(`{"schema":`),
				[]byte(`{"schema":"mmwcli.multisensor_session.v1","schema":`),
				1,
			),
			match: "duplicate JSON object key",
		},
		{name: "trailing value", encoded: append(append([]byte(nil), encoded...), []byte(`[]`)...), match: "trailing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := UnmarshalSession(test.encoded); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("UnmarshalSession error = %v, want %q", err, test.match)
			}
		})
	}
}

func twoSourceFixture(t *testing.T) (Session, map[string]SensorIndex, map[string][]byte) {
	t.Helper()
	limits := SourceLimits{MaxItems: 16, MaxItemBytes: 16, MaxPayloadBytes: 1024}
	payloads := map[string][]byte{
		"radar-0":  {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		"camera-0": []byte("camera-frame"),
	}
	indexes := map[string]SensorIndex{
		"radar-0": {
			PayloadBytes: 16,
			Entries: []IndexEntry{
				{ItemIndex: 0, PayloadOffset: 0, PayloadSize: 8, Tick: 100, DurationTicks: 10, SyncEventID: NoSyncEventID},
				{ItemIndex: 1, PayloadOffset: 8, PayloadSize: 8, Tick: 200, DurationTicks: 10, SyncEventID: NoSyncEventID},
			},
		},
		"camera-0": {
			PayloadBytes: 12,
			Entries: []IndexEntry{
				{ItemIndex: 0, PayloadOffset: 0, PayloadSize: 6, Tick: 10, DurationTicks: 2, SyncEventID: NoSyncEventID},
				{ItemIndex: 1, PayloadOffset: 6, PayloadSize: 6, Tick: 20, DurationTicks: 2, SyncEventID: NoSyncEventID},
			},
		},
	}
	session := Session{
		Schema:               SessionSchema,
		SessionID:            "123e4567-e89b-42d3-a456-426614174000",
		SynchronizationGrade: SynchronizationSoftwareBarrier,
		HostClock: Clock{
			ClockID: "host-monotonic", TickHz: 1_000_000_000,
			TimestampSemantics: TimestampHostMonotonic,
		},
		SyncEvents: []SyncEvent{},
		Totals: AggregateTotals{
			SourceCount: 2, RequiredSourceCount: 2, CompleteSourceCount: 2,
			ItemCount: 4, PayloadBytes: 28,
		},
		ApplicationMetadata: ApplicationMetadata{
			"org.openmmw.training": json.RawMessage(`{"split":"golden"}`),
		},
	}
	session.Sources = []Source{
		fixtureSource(
			t,
			"radar-0",
			SourceRadar,
			Producer{Name: "mmwcli", Version: "1.0.0"},
			limits,
			PayloadContract{Filename: "adc.bin", Format: "ti_mmwave_legacy_cli.v1"},
			Clock{ClockID: "radar-frame-clock", TickHz: 1_000_000, TimestampSemantics: TimestampFrameStart},
			ClockObservation{
				ObservationID: "radar-map-0", Tick: 0,
				HostBeforeNS: 999_995_000, HostAfterNS: 1_000_005_000,
			},
			AffineSegment{
				StartUnwrappedTick: 0, EndUnwrappedTick: 1000, SourceOriginTick: 0,
				HostOriginNS: 1_000_000_000, ScaleNum: 1000, ScaleDen: 1,
				ObservationIDs: []string{"radar-map-0"}, UncertaintyNS: 5000,
			},
			indexes["radar-0"],
			payloads["radar-0"],
			ApplicationMetadata{},
		),
		fixtureSource(
			t,
			"camera-0",
			SourceCamera,
			Producer{Name: "fake-camera", Version: "1.0.0"},
			limits,
			PayloadContract{Filename: "frames.bin", Format: "rgb8.v1"},
			Clock{ClockID: "camera-exposure-clock", TickHz: 1000, TimestampSemantics: TimestampExposureMidpoint},
			ClockObservation{
				ObservationID: "camera-map-0", Tick: 0,
				HostBeforeNS: 999_980_000, HostAfterNS: 1_000_020_000,
			},
			AffineSegment{
				StartUnwrappedTick: 0, EndUnwrappedTick: 1000, SourceOriginTick: 0,
				HostOriginNS: 1_000_000_000, ScaleNum: 1_000_000, ScaleDen: 1,
				ObservationIDs: []string{"camera-map-0"}, UncertaintyNS: 20_000,
			},
			indexes["camera-0"],
			payloads["camera-0"],
			ApplicationMetadata{
				"org.openmmw.camera": json.RawMessage(`{"pixel_format":"rgb8"}`),
			},
		),
	}
	if err := session.ValidateWithIndexes(indexes); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	return session, indexes, payloads
}

func fixtureSource(
	t *testing.T,
	sourceID string,
	kind SourceKind,
	producer Producer,
	limits SourceLimits,
	payloadContract PayloadContract,
	clock Clock,
	observation ClockObservation,
	segment AffineSegment,
	index SensorIndex,
	payload []byte,
	metadata ApplicationMetadata,
) Source {
	t.Helper()
	indexBytes, err := EncodeSensorIndex(index, limits)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := sha256.Sum256(payload)
	indexDigest := sha256.Sum256(indexBytes)
	return Source{
		SourceID: sourceID, Kind: kind, Required: true, Outcome: OutcomeComplete,
		Producer: producer, Limits: limits, Payload: payloadContract,
		ItemCount: uint64(len(index.Entries)), PayloadBytes: uint64(len(payload)), Clock: clock,
		ClockObservations: []ClockObservation{observation},
		AffineSegments:    []AffineSegment{segment},
		Artifacts: []Artifact{
			{
				Role: ArtifactPayload, Path: payloadContract.Filename, SizeBytes: uint64(len(payload)),
				SHA256: hex.EncodeToString(payloadDigest[:]),
			},
			{
				Role: ArtifactIndex, Path: "index.bin", SizeBytes: uint64(len(indexBytes)),
				SHA256: hex.EncodeToString(indexDigest[:]),
			},
		},
		ApplicationMetadata: metadata,
	}
}

func sourceByID(t *testing.T, session Session, sourceID string) Source {
	t.Helper()
	for _, source := range session.Sources {
		if source.SourceID == sourceID {
			return source
		}
	}
	t.Fatalf("source %q not found", sourceID)
	return Source{}
}

func artifactForRole(t *testing.T, source Source, role ArtifactRole) Artifact {
	t.Helper()
	artifact, err := artifactByRole(source.Artifacts, role)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func cloneSession(t *testing.T, session Session) Session {
	t.Helper()
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var cloned Session
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func cloneIndexes(indexes map[string]SensorIndex) map[string]SensorIndex {
	cloned := make(map[string]SensorIndex, len(indexes))
	for sourceID, index := range indexes {
		index.Entries = append([]IndexEntry(nil), index.Entries...)
		cloned[sourceID] = index
	}
	return cloned
}

func bindIndexArtifact(t *testing.T, source *Source, index SensorIndex) {
	t.Helper()
	encoded, err := EncodeSensorIndex(index, source.Limits)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	for artifactIndex := range source.Artifacts {
		if source.Artifacts[artifactIndex].Role == ArtifactIndex {
			source.Artifacts[artifactIndex].SizeBytes = uint64(len(encoded))
			source.Artifacts[artifactIndex].SHA256 = hex.EncodeToString(digest[:])
			return
		}
	}
	t.Fatal(errors.New("missing index artifact"))
}

func TestSessionRequiredOutcomesArtifactsClocksAndMetadataAreStrict(t *testing.T) {
	base, indexes, _ := twoSourceFixture(t)
	tests := []struct {
		name   string
		mutate func(*Session)
		match  string
	}{
		{
			name: "required failed",
			mutate: func(session *Session) {
				session.Sources[0].Outcome = OutcomeFailed
			},
			match: "required source",
		},
		{
			name: "duplicate source",
			mutate: func(session *Session) {
				session.Sources[1].SourceID = session.Sources[0].SourceID
			},
			match: "duplicate source_id",
		},
		{
			name: "duplicate clock",
			mutate: func(session *Session) {
				session.Sources[1].Clock.ClockID = session.Sources[0].Clock.ClockID
			},
			match: "duplicate clock_id",
		},
		{
			name: "unsafe artifact",
			mutate: func(session *Session) {
				session.Sources[0].Artifacts[0].Path = "../adc.bin"
			},
			match: "safe fixed leaf",
		},
		{
			name: "unknown outcome",
			mutate: func(session *Session) {
				session.Sources[0].Outcome = "partial"
			},
			match: "unsupported source outcome",
		},
		{
			name: "bad semantics",
			mutate: func(session *Session) {
				session.Sources[0].Clock.TimestampSemantics = TimestampExposureMidpoint
			},
			match: "frame_start",
		},
		{
			name: "observation interval reversed",
			mutate: func(session *Session) {
				session.Sources[0].ClockObservations[0].HostBeforeNS = 2
				session.Sources[0].ClockObservations[0].HostAfterNS = 1
			},
			match: "host_before_ns after",
		},
		{
			name: "overlapping affine segments",
			mutate: func(session *Session) {
				segment := session.Sources[0].AffineSegments[0]
				session.Sources[0].AffineSegments = append(session.Sources[0].AffineSegments, segment)
			},
			match: "overlaps",
		},
		{
			name: "unknown affine observation",
			mutate: func(session *Session) {
				session.Sources[0].AffineSegments[0].ObservationIDs[0] = "missing"
			},
			match: "unknown observation_id",
		},
		{
			name: "affine observation outside segment",
			mutate: func(session *Session) {
				session.Sources[0].ClockObservations[0].Tick = 1000
			},
			match: "outside its tick range",
		},
		{
			name: "affine uncertainty misses observation",
			mutate: func(session *Session) {
				session.Sources[0].ClockObservations[0].HostAfterNS++
			},
			match: "does not cover observation",
		},
		{
			name: "flat metadata key",
			mutate: func(session *Session) {
				session.ApplicationMetadata["training"] = json.RawMessage(`true`)
			},
			match: "not namespaced",
		},
		{
			name: "duplicate metadata JSON key",
			mutate: func(session *Session) {
				session.ApplicationMetadata["org.openmmw.training"] = json.RawMessage(`{"x":1,"x":2}`)
			},
			match: "duplicate JSON object key",
		},
		{
			name: "wrong totals",
			mutate: func(session *Session) {
				session.Totals.ItemCount++
			},
			match: "computed totals",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := cloneSession(t, base)
			test.mutate(&session)
			if err := session.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate error = %v, want %q", err, test.match)
			}
		})
	}

	optional := cloneSession(t, base)
	optional.Sources[1].Required = false
	optional.Sources[1].Outcome = OutcomeOmitted
	optional.Sources[1].ItemCount = 0
	optional.Sources[1].PayloadBytes = 0
	optional.Sources[1].Artifacts = []Artifact{}
	optional.Totals = AggregateTotals{
		SourceCount: 2, RequiredSourceCount: 1, CompleteSourceCount: 1,
		ItemCount: 2, PayloadBytes: 16,
	}
	if err := optional.ValidateWithIndexes(map[string]SensorIndex{"radar-0": indexes["radar-0"]}); err != nil {
		t.Fatalf("valid optional omission: %v", err)
	}
}

func TestDeliveryObservedCameraClockContract(t *testing.T) {
	base, baseIndexes, _ := twoSourceFixture(t)
	session := cloneSession(t, base)
	indexes := cloneIndexes(baseIndexes)
	camera := &session.Sources[1]
	camera.Clock = Clock{
		ClockID: DeliveryObservedClockID(camera.SourceID), TickHz: 1_000_000_000,
		TimestampSemantics: TimestampDeliveryObserved,
	}
	camera.ClockObservations = []ClockObservation{{
		ObservationID: "camera-0-delivery-anchor", Tick: 10,
		HostBeforeNS: 10, HostAfterNS: 10,
	}}
	camera.AffineSegments = []AffineSegment{{
		StartUnwrappedTick: 10, EndUnwrappedTick: 21, SourceOriginTick: 10,
		HostOriginNS: 10, ScaleNum: 1, ScaleDen: 1,
		ObservationIDs: []string{"camera-0-delivery-anchor"},
	}}
	index := indexes[camera.SourceID]
	for entryIndex := range index.Entries {
		index.Entries[entryIndex].DurationTicks = 0
	}
	indexes[camera.SourceID] = index
	bindIndexArtifact(t, camera, index)
	if err := session.ValidateWithIndexes(indexes); err != nil {
		t.Fatalf("valid delivery_observed camera source: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Clock)
	}{
		{name: "clock id", mutate: func(clock *Clock) { clock.ClockID = "camera-delivery" }},
		{name: "tick rate", mutate: func(clock *Clock) { clock.TickHz = 1_000_000 }},
		{name: "wrapping", mutate: func(clock *Clock) { clock.WrapTicks = 1 << 32 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := cloneSession(t, session)
			test.mutate(&mutated.Sources[1].Clock)
			if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "dedicated clock_id") {
				t.Fatalf("Validate error = %v, want dedicated delivery_observed clock contract", err)
			}
		})
	}
}

func TestSessionIndexCoverageAndArtifactDigestAreClosed(t *testing.T) {
	base, baseIndexes, _ := twoSourceFixture(t)
	tests := []struct {
		name   string
		mutate func(*Session, map[string]SensorIndex)
		match  string
	}{
		{
			name: "missing index",
			mutate: func(_ *Session, indexes map[string]SensorIndex) {
				delete(indexes, "camera-0")
			},
			match: "has no sensor index",
		},
		{
			name: "unknown index",
			mutate: func(_ *Session, indexes map[string]SensorIndex) {
				indexes["other-0"] = indexes["camera-0"]
			},
			match: "undeclared source",
		},
		{
			name: "tick outside mapping",
			mutate: func(session *Session, indexes map[string]SensorIndex) {
				index := indexes["radar-0"]
				index.Entries[1].Tick = 1000
				indexes["radar-0"] = index
				bindIndexArtifact(t, &session.Sources[0], index)
			},
			match: "not covered",
		},
		{
			name: "clock moves backwards",
			mutate: func(session *Session, indexes map[string]SensorIndex) {
				index := indexes["radar-0"]
				index.Entries[1].Tick = 50
				indexes["radar-0"] = index
				bindIndexArtifact(t, &session.Sources[0], index)
			},
			match: "moves backwards",
		},
		{
			name: "digest mismatch",
			mutate: func(session *Session, _ map[string]SensorIndex) {
				for artifactIndex := range session.Sources[0].Artifacts {
					if session.Sources[0].Artifacts[artifactIndex].Role == ArtifactIndex {
						session.Sources[0].Artifacts[artifactIndex].SHA256 = strings.Repeat("0", 64)
					}
				}
			},
			match: "sha256 disagrees",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := cloneSession(t, base)
			indexes := cloneIndexes(baseIndexes)
			test.mutate(&session, indexes)
			if err := session.ValidateWithIndexes(indexes); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("ValidateWithIndexes error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestDeclaredSynchronizationEventsAreStrict(t *testing.T) {
	session, indexes, _ := twoSourceFixture(t)
	session.SyncEvents = []SyncEvent{
		{
			SyncEventID: 7, ClockID: "radar-frame-clock", Tick: 100, Edge: EventRising,
			EvidenceKind: EvidenceHardwareObservation, Generator: "trigger-gen",
			Observer: "radar-observer", RoutingID: "route-0",
			ObservationIDs: []string{"radar-map-0"}, UncertaintyNS: 5000,
		},
		{
			SyncEventID: 8, ClockID: "radar-frame-clock", Tick: 200, Edge: EventRising,
			EvidenceKind: EvidenceTriggerGeneration, Generator: "trigger-gen",
			Observer: "radar-observer", RoutingID: "route-0",
			ObservationIDs: []string{"radar-map-0"}, UncertaintyNS: 5000,
		},
	}
	for sourceIndex := range session.Sources {
		session.Sources[sourceIndex].SyncEventCardinality = &EventCardinality{
			Required: true, MinItems: 1, MaxItems: 1,
		}
		index := indexes[session.Sources[sourceIndex].SourceID]
		index.Entries[0].SyncEventID = 7
		index.Entries[1].SyncEventID = 8
		indexes[session.Sources[sourceIndex].SourceID] = index
		bindIndexArtifact(t, &session.Sources[sourceIndex], index)
	}
	if err := session.ValidateWithIndexes(indexes); err != nil {
		t.Fatalf("valid event ledger: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Session, map[string]SensorIndex)
		match  string
	}{
		{
			name: "unknown event reference",
			mutate: func(session *Session, indexes map[string]SensorIndex) {
				index := indexes["camera-0"]
				index.Entries[1].SyncEventID = 9
				indexes["camera-0"] = index
				bindIndexArtifact(t, &session.Sources[1], index)
			},
			match: "unknown sync_event_id",
		},
		{
			name: "unexpected duplicate",
			mutate: func(session *Session, indexes map[string]SensorIndex) {
				index := indexes["camera-0"]
				index.Entries[1].SyncEventID = 7
				indexes["camera-0"] = index
				bindIndexArtifact(t, &session.Sources[1], index)
			},
			match: "outside declared cardinality",
		},
		{
			name: "missing association",
			mutate: func(session *Session, indexes map[string]SensorIndex) {
				index := indexes["camera-0"]
				index.Entries[1].SyncEventID = NoSyncEventID
				indexes["camera-0"] = index
				bindIndexArtifact(t, &session.Sources[1], index)
			},
			match: "omits a required sync event",
		},
		{
			name: "unknown edge",
			mutate: func(session *Session, _ map[string]SensorIndex) {
				session.SyncEvents[0].Edge = "level"
			},
			match: "unsupported edge",
		},
		{
			name: "event outside mapping",
			mutate: func(session *Session, _ map[string]SensorIndex) {
				session.SyncEvents[0].Tick = 1000
			},
			match: "not covered",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutatedSession := cloneSession(t, session)
			mutatedIndexes := cloneIndexes(indexes)
			test.mutate(&mutatedSession, mutatedIndexes)
			if err := mutatedSession.ValidateWithIndexes(mutatedIndexes); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("ValidateWithIndexes error = %v, want %q", err, test.match)
			}
		})
	}
}

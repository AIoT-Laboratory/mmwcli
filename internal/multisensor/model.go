package multisensor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SessionSchema = "mmwcli.multisensor_session.v1"

	MaximumSessionJSONBytes            = 1 << 20
	MaximumSessionJSONDepth            = 32
	MaximumSources                     = 32
	MaximumArtifactsPerSource          = 16
	MaximumClockObservationsPerSource  = 4096
	MaximumAffineSegmentsPerSource     = 1024
	MaximumSyncEvents                  = 1 << 20
	MaximumApplicationMetadataEntries  = 32
	MaximumApplicationMetadataBytes    = 64 << 10
	MaximumApplicationMetadataDepth    = 16
	MaximumApplicationMetadataKeyBytes = 128
	MaximumSourceIDBytes               = 64
	MaximumOpaqueIDBytes               = 128
	MaximumPayloadFormatBytes          = 128
	MaximumArtifactPathBytes           = 128
)

type SynchronizationGrade string

const SynchronizationSoftwareBarrier SynchronizationGrade = "software_barrier"

type SourceKind string

const (
	SourceRadar  SourceKind = "radar"
	SourceCamera SourceKind = "camera"
)

type SourceOutcome string

const (
	OutcomeComplete SourceOutcome = "complete"
	OutcomeFailed   SourceOutcome = "failed"
	OutcomeOmitted  SourceOutcome = "omitted"
)

type TimestampSemantics string

const (
	TimestampHostMonotonic    TimestampSemantics = "host_monotonic"
	TimestampFrameStart       TimestampSemantics = "frame_start"
	TimestampExposureMidpoint TimestampSemantics = "exposure_midpoint"
	TimestampDeliveryObserved TimestampSemantics = "delivery_observed"
)

// DeliveryObservedClockID returns the only clock_id accepted for one camera
// whose timestamps are assigned by the aggregate recorder on full delivery.
func DeliveryObservedClockID(sourceID string) string { return sourceID + "-delivery-observed" }

type ArtifactRole string

const (
	ArtifactPayload       ArtifactRole = "payload"
	ArtifactIndex         ArtifactRole = "index"
	ArtifactConfiguration ArtifactRole = "configuration"
	ArtifactManifest      ArtifactRole = "manifest"
	ArtifactMetadata      ArtifactRole = "metadata"
)

type EventEdge string

const (
	EventRising  EventEdge = "rising"
	EventFalling EventEdge = "falling"
)

type EventEvidenceKind string

const (
	EvidenceTriggerGeneration   EventEvidenceKind = "trigger_generation"
	EvidenceHardwareObservation EventEvidenceKind = "hardware_observation"
)

type ApplicationMetadata map[string]json.RawMessage

type Session struct {
	Schema               string               `json:"schema"`
	SessionID            string               `json:"session_id"`
	SynchronizationGrade SynchronizationGrade `json:"synchronization_grade"`
	HostClock            Clock                `json:"host_clock"`
	Sources              []Source             `json:"sources"`
	SyncEvents           []SyncEvent          `json:"sync_events"`
	Totals               AggregateTotals      `json:"totals"`
	ApplicationMetadata  ApplicationMetadata  `json:"application_metadata"`
}

type Source struct {
	SourceID             string              `json:"source_id"`
	Kind                 SourceKind          `json:"kind"`
	Required             bool                `json:"required"`
	Outcome              SourceOutcome       `json:"outcome"`
	Producer             Producer            `json:"producer"`
	Limits               SourceLimits        `json:"limits"`
	Payload              PayloadContract     `json:"payload"`
	ItemCount            uint64              `json:"item_count"`
	PayloadBytes         uint64              `json:"payload_bytes"`
	Clock                Clock               `json:"clock"`
	ClockObservations    []ClockObservation  `json:"clock_observations"`
	AffineSegments       []AffineSegment     `json:"affine_segments"`
	SyncEventCardinality *EventCardinality   `json:"sync_event_cardinality,omitempty"`
	Artifacts            []Artifact          `json:"artifacts"`
	ApplicationMetadata  ApplicationMetadata `json:"application_metadata"`
}

type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type SourceLimits struct {
	MaxItems        uint64 `json:"max_items"`
	MaxItemBytes    uint64 `json:"max_item_bytes"`
	MaxPayloadBytes uint64 `json:"max_payload_bytes"`
}

type PayloadContract struct {
	Filename string `json:"filename"`
	Format   string `json:"format"`
}

type Clock struct {
	ClockID            string             `json:"clock_id"`
	TickHz             uint64             `json:"tick_hz"`
	WrapTicks          uint64             `json:"wrap_ticks"`
	TimestampSemantics TimestampSemantics `json:"timestamp_semantics"`
}

type ClockObservation struct {
	ObservationID string `json:"observation_id"`
	Tick          uint64 `json:"tick"`
	WrapCount     uint64 `json:"wrap_count"`
	HostBeforeNS  uint64 `json:"host_before_ns"`
	HostAfterNS   uint64 `json:"host_after_ns"`
}

type AffineSegment struct {
	StartUnwrappedTick uint64   `json:"start_unwrapped_tick"`
	EndUnwrappedTick   uint64   `json:"end_unwrapped_tick"`
	SourceOriginTick   uint64   `json:"source_origin_tick"`
	HostOriginNS       uint64   `json:"host_origin_ns"`
	ScaleNum           uint64   `json:"scale_num"`
	ScaleDen           uint64   `json:"scale_den"`
	ObservationIDs     []string `json:"observation_ids"`
	UncertaintyNS      uint64   `json:"uncertainty_ns"`
}

type EventCardinality struct {
	Required bool   `json:"required"`
	MinItems uint64 `json:"min_items"`
	MaxItems uint64 `json:"max_items"`
}

type Artifact struct {
	Role      ArtifactRole `json:"role"`
	Path      string       `json:"path"`
	SizeBytes uint64       `json:"size_bytes"`
	SHA256    string       `json:"sha256"`
}

type SyncEvent struct {
	SyncEventID    uint64            `json:"sync_event_id"`
	ClockID        string            `json:"clock_id"`
	Tick           uint64            `json:"tick"`
	WrapCount      uint64            `json:"wrap_count"`
	Edge           EventEdge         `json:"edge"`
	EvidenceKind   EventEvidenceKind `json:"evidence_kind"`
	Generator      string            `json:"generator"`
	Observer       string            `json:"observer"`
	RoutingID      string            `json:"routing_id"`
	ObservationIDs []string          `json:"observation_ids"`
	UncertaintyNS  uint64            `json:"uncertainty_ns"`
}

type AggregateTotals struct {
	SourceCount         uint64 `json:"source_count"`
	RequiredSourceCount uint64 `json:"required_source_count"`
	CompleteSourceCount uint64 `json:"complete_source_count"`
	ItemCount           uint64 `json:"item_count"`
	PayloadBytes        uint64 `json:"payload_bytes"`
}

var (
	sessionIDPattern = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
	sourceIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	opaqueIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	formatPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	metadataKeyPattern = regexp.MustCompile(
		`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*){2,}$`,
	)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// MarshalSession emits deterministic compact JSON after validating the closed
// v1 model. Go's encoding/json orders metadata map keys lexicographically.
func MarshalSession(session Session) ([]byte, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("encode multisensor session: %w", err)
	}
	if len(encoded) > MaximumSessionJSONBytes {
		return nil, fmt.Errorf(
			"multisensor session JSON is %d bytes, maximum is %d",
			len(encoded),
			MaximumSessionJSONBytes,
		)
	}
	return encoded, nil
}

// UnmarshalSession rejects unknown fields, duplicate keys, excessive nesting,
// trailing values, and every invalid closed-contract value.
func UnmarshalSession(encoded []byte) (Session, error) {
	if len(encoded) == 0 || len(encoded) > MaximumSessionJSONBytes {
		return Session{}, fmt.Errorf(
			"multisensor session JSON size %d is outside 1..%d",
			len(encoded),
			MaximumSessionJSONBytes,
		)
	}
	if err := validateJSONDocument(encoded, MaximumSessionJSONDepth); err != nil {
		return Session{}, fmt.Errorf("invalid multisensor session JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var session Session
	if err := decoder.Decode(&session); err != nil {
		return Session{}, fmt.Errorf("decode multisensor session: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Session{}, err
	}
	if err := session.Validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

// Validate checks the self-contained JSON contract. Use ValidateWithIndexes to
// bind complete sources to decoded index bytes and event associations.
func (session Session) Validate() error {
	if session.Schema != SessionSchema {
		return fmt.Errorf("multisensor session schema is %q, want %q", session.Schema, SessionSchema)
	}
	if !sessionIDPattern.MatchString(session.SessionID) {
		return errors.New("session_id must be a lowercase UUIDv4")
	}
	if session.SynchronizationGrade != SynchronizationSoftwareBarrier {
		return fmt.Errorf(
			"unsupported synchronization_grade %q; v1 currently publishes only software_barrier evidence",
			session.SynchronizationGrade,
		)
	}
	if err := validateHostClock(session.HostClock); err != nil {
		return err
	}
	if session.Sources == nil || len(session.Sources) == 0 || len(session.Sources) > MaximumSources {
		return fmt.Errorf("sources count %d is outside 1..%d", len(session.Sources), MaximumSources)
	}
	if session.SyncEvents == nil || len(session.SyncEvents) > MaximumSyncEvents {
		return fmt.Errorf("sync_events must be an array with at most %d entries", MaximumSyncEvents)
	}
	if err := validateApplicationMetadata("session", session.ApplicationMetadata); err != nil {
		return err
	}

	clockDomains := map[string]*Source{session.HostClock.ClockID: nil}
	sourceIDs := make(map[string]struct{}, len(session.Sources))
	computed := AggregateTotals{SourceCount: uint64(len(session.Sources))}
	for sourceIndex := range session.Sources {
		source := &session.Sources[sourceIndex]
		if _, duplicate := sourceIDs[source.SourceID]; duplicate {
			return fmt.Errorf("duplicate source_id %q", source.SourceID)
		}
		sourceIDs[source.SourceID] = struct{}{}
		if _, duplicate := clockDomains[source.Clock.ClockID]; duplicate {
			return fmt.Errorf("duplicate clock_id %q", source.Clock.ClockID)
		}
		if err := validateSource(*source); err != nil {
			return fmt.Errorf("source %q: %w", source.SourceID, err)
		}
		clockDomains[source.Clock.ClockID] = source
		if source.Required {
			computed.RequiredSourceCount++
		}
		if source.Outcome == OutcomeComplete {
			computed.CompleteSourceCount++
			var ok bool
			computed.ItemCount, ok = checkedAddU64(computed.ItemCount, source.ItemCount)
			if !ok {
				return errors.New("aggregate item_count overflows uint64")
			}
			computed.PayloadBytes, ok = checkedAddU64(computed.PayloadBytes, source.PayloadBytes)
			if !ok {
				return errors.New("aggregate payload_bytes overflows uint64")
			}
		}
	}
	if session.Totals != computed {
		return fmt.Errorf("aggregate totals %+v do not equal computed totals %+v", session.Totals, computed)
	}
	if err := validateSyncEvents(session.SyncEvents, clockDomains); err != nil {
		return err
	}
	return nil
}

// ValidateWithIndexes closes the session/index relationship for every source.
func (session Session) ValidateWithIndexes(indexes map[string]SensorIndex) error {
	if err := session.Validate(); err != nil {
		return err
	}
	completeSources := 0
	sources := make(map[string]Source, len(session.Sources))
	events := make(map[uint64]SyncEvent, len(session.SyncEvents))
	for _, event := range session.SyncEvents {
		events[event.SyncEventID] = event
	}
	for _, source := range session.Sources {
		sources[source.SourceID] = source
		if source.Outcome != OutcomeComplete {
			if _, exists := indexes[source.SourceID]; exists {
				return fmt.Errorf("non-complete source %q has a published sensor index", source.SourceID)
			}
			continue
		}
		completeSources++
		index, exists := indexes[source.SourceID]
		if !exists {
			return fmt.Errorf("complete source %q has no sensor index", source.SourceID)
		}
		if err := validateSourceIndex(source, index, events); err != nil {
			return fmt.Errorf("source %q index: %w", source.SourceID, err)
		}
	}
	if len(indexes) != completeSources {
		for sourceID := range indexes {
			if _, exists := sources[sourceID]; !exists {
				return fmt.Errorf("sensor index names undeclared source %q", sourceID)
			}
		}
		return fmt.Errorf("got %d sensor indices for %d complete sources", len(indexes), completeSources)
	}
	return nil
}

func (limits SourceLimits) Validate() error {
	if limits.MaxItems == 0 || limits.MaxItems > MaximumIndexItems {
		return fmt.Errorf("max_items %d is outside 1..%d", limits.MaxItems, MaximumIndexItems)
	}
	if limits.MaxItemBytes == 0 || limits.MaxItemBytes > MaximumItemBytes {
		return fmt.Errorf("max_item_bytes %d is outside 1..%d", limits.MaxItemBytes, MaximumItemBytes)
	}
	if limits.MaxPayloadBytes == 0 || limits.MaxPayloadBytes > MaximumPayloadBytes {
		return fmt.Errorf(
			"max_payload_bytes %d is outside 1..%d",
			limits.MaxPayloadBytes,
			MaximumPayloadBytes,
		)
	}
	if limits.MaxItemBytes > limits.MaxPayloadBytes {
		return errors.New("max_item_bytes exceeds max_payload_bytes")
	}
	return nil
}

// UnwrapTicks performs the explicit checked wrap arithmetic shared by clocks,
// index entries, observations, and synchronization events.
func UnwrapTicks(clock Clock, tick, wrapCount uint64) (uint64, error) {
	if clock.WrapTicks == 0 {
		if wrapCount != 0 {
			return 0, errors.New("wrap_count must be zero for a non-wrapping clock")
		}
		return tick, nil
	}
	if tick >= clock.WrapTicks {
		return 0, fmt.Errorf("tick %d is not below wrap_ticks %d", tick, clock.WrapTicks)
	}
	wrapped, ok := checkedMulU64(wrapCount, clock.WrapTicks)
	if !ok {
		return 0, errors.New("wrap_count * wrap_ticks overflows uint64")
	}
	unwrapped, ok := checkedAddU64(wrapped, tick)
	if !ok {
		return 0, errors.New("unwrapped tick overflows uint64")
	}
	return unwrapped, nil
}

func validateHostClock(clock Clock) error {
	if err := validateClock(clock); err != nil {
		return fmt.Errorf("host_clock: %w", err)
	}
	if clock.TickHz != 1_000_000_000 || clock.WrapTicks != 0 ||
		clock.TimestampSemantics != TimestampHostMonotonic {
		return errors.New(
			"host_clock must use tick_hz=1000000000, wrap_ticks=0, and host_monotonic semantics",
		)
	}
	return nil
}

func validateClock(clock Clock) error {
	if err := validateOpaqueID("clock_id", clock.ClockID); err != nil {
		return err
	}
	if clock.TickHz == 0 {
		return errors.New("tick_hz must be positive")
	}
	if clock.WrapTicks == 1 {
		return errors.New("wrap_ticks must be zero or greater than one")
	}
	switch clock.TimestampSemantics {
	case TimestampHostMonotonic, TimestampFrameStart, TimestampExposureMidpoint, TimestampDeliveryObserved:
		return nil
	default:
		return fmt.Errorf("unsupported timestamp_semantics %q", clock.TimestampSemantics)
	}
}

func validateSource(source Source) error {
	if len(source.SourceID) > MaximumSourceIDBytes || !sourceIDPattern.MatchString(source.SourceID) {
		return fmt.Errorf("source_id %q is not a safe lowercase directory leaf", source.SourceID)
	}
	switch source.Kind {
	case SourceRadar:
		if source.Clock.TimestampSemantics != TimestampFrameStart {
			return errors.New("radar source clock must use frame_start semantics")
		}
	case SourceCamera:
		switch source.Clock.TimestampSemantics {
		case TimestampExposureMidpoint:
		case TimestampDeliveryObserved:
			if source.Clock.ClockID != DeliveryObservedClockID(source.SourceID) ||
				source.Clock.TickHz != 1_000_000_000 || source.Clock.WrapTicks != 0 {
				return errors.New(
					"delivery_observed camera clock must use its dedicated clock_id, tick_hz=1000000000, and wrap_ticks=0",
				)
			}
		default:
			return errors.New("camera source clock must use exposure_midpoint or delivery_observed semantics")
		}
	default:
		return fmt.Errorf("unsupported source kind %q", source.Kind)
	}
	switch source.Outcome {
	case OutcomeComplete:
	case OutcomeFailed, OutcomeOmitted:
		if source.Required {
			return fmt.Errorf("required source cannot publish outcome %q", source.Outcome)
		}
	default:
		return fmt.Errorf("unsupported source outcome %q", source.Outcome)
	}
	if err := validateOpaqueID("producer.name", source.Producer.Name); err != nil {
		return err
	}
	if err := validateOpaqueID("producer.version", source.Producer.Version); err != nil {
		return err
	}
	if err := source.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if err := validateArtifactLeaf("payload.filename", source.Payload.Filename); err != nil {
		return err
	}
	if source.Payload.Filename == "index.bin" {
		return errors.New("payload.filename cannot be index.bin")
	}
	if len(source.Payload.Format) > MaximumPayloadFormatBytes || !formatPattern.MatchString(source.Payload.Format) {
		return fmt.Errorf("payload.format %q is invalid", source.Payload.Format)
	}
	if source.ItemCount > source.Limits.MaxItems || source.PayloadBytes > source.Limits.MaxPayloadBytes {
		return errors.New("published source counts exceed declared limits")
	}
	if (source.ItemCount == 0) != (source.PayloadBytes == 0) {
		return errors.New("item_count and payload_bytes must either both be zero or both be nonzero")
	}
	if source.Outcome != OutcomeComplete && (source.ItemCount != 0 || source.PayloadBytes != 0) {
		return errors.New("failed or omitted source must publish zero items and payload bytes")
	}
	if err := validateClock(source.Clock); err != nil {
		return fmt.Errorf("clock: %w", err)
	}
	if source.Clock.TimestampSemantics == TimestampHostMonotonic {
		return errors.New("source clock cannot claim host_monotonic semantics")
	}
	if source.ClockObservations == nil || len(source.ClockObservations) > MaximumClockObservationsPerSource {
		return fmt.Errorf(
			"clock_observations must be an array with at most %d entries",
			MaximumClockObservationsPerSource,
		)
	}
	observations, err := validateClockObservations(source.Clock, source.ClockObservations)
	if err != nil {
		return err
	}
	if source.AffineSegments == nil || len(source.AffineSegments) > MaximumAffineSegmentsPerSource {
		return fmt.Errorf(
			"affine_segments must be an array with at most %d entries",
			MaximumAffineSegmentsPerSource,
		)
	}
	if source.Outcome == OutcomeComplete && source.ItemCount > 0 && len(source.AffineSegments) == 0 {
		return errors.New("complete nonempty source requires an affine segment")
	}
	if err := validateAffineSegments(source.Clock, source.AffineSegments, observations); err != nil {
		return err
	}
	if source.SyncEventCardinality != nil {
		cardinality := *source.SyncEventCardinality
		if cardinality.MaxItems == 0 || cardinality.MaxItems > source.Limits.MaxItems ||
			cardinality.MinItems > cardinality.MaxItems {
			return errors.New("sync_event_cardinality has invalid min_items/max_items")
		}
	}
	if source.Artifacts == nil || len(source.Artifacts) > MaximumArtifactsPerSource {
		return fmt.Errorf("artifacts must be an array with at most %d entries", MaximumArtifactsPerSource)
	}
	if err := validateArtifacts(source); err != nil {
		return err
	}
	if err := validateApplicationMetadata("source "+source.SourceID, source.ApplicationMetadata); err != nil {
		return err
	}
	return nil
}

func validateClockObservations(
	clock Clock,
	items []ClockObservation,
) (map[string]ClockObservation, error) {
	observations := make(map[string]ClockObservation, len(items))
	for index, observation := range items {
		if err := validateOpaqueID("observation_id", observation.ObservationID); err != nil {
			return nil, fmt.Errorf("clock observation %d: %w", index, err)
		}
		if _, duplicate := observations[observation.ObservationID]; duplicate {
			return nil, fmt.Errorf("duplicate observation_id %q", observation.ObservationID)
		}
		if observation.HostBeforeNS > observation.HostAfterNS {
			return nil, fmt.Errorf("clock observation %q has host_before_ns after host_after_ns", observation.ObservationID)
		}
		if _, err := UnwrapTicks(clock, observation.Tick, observation.WrapCount); err != nil {
			return nil, fmt.Errorf("clock observation %q: %w", observation.ObservationID, err)
		}
		observations[observation.ObservationID] = observation
	}
	return observations, nil
}

func validateAffineSegments(
	clock Clock,
	segments []AffineSegment,
	observations map[string]ClockObservation,
) error {
	var previous *AffineSegment
	for index := range segments {
		segment := &segments[index]
		if segment.StartUnwrappedTick >= segment.EndUnwrappedTick {
			return fmt.Errorf("affine segment %d has an empty or reversed range", index)
		}
		if segment.SourceOriginTick < segment.StartUnwrappedTick ||
			segment.SourceOriginTick >= segment.EndUnwrappedTick {
			return fmt.Errorf("affine segment %d source_origin_tick is outside its range", index)
		}
		if segment.ScaleNum == 0 || segment.ScaleDen == 0 {
			return fmt.Errorf("affine segment %d scale must be a positive rational", index)
		}
		if segment.ObservationIDs == nil || len(segment.ObservationIDs) == 0 {
			return fmt.Errorf("affine segment %d has no supporting observations", index)
		}
		seen := make(map[string]struct{}, len(segment.ObservationIDs))
		for _, observationID := range segment.ObservationIDs {
			if _, duplicate := seen[observationID]; duplicate {
				return fmt.Errorf("affine segment %d repeats observation_id %q", index, observationID)
			}
			seen[observationID] = struct{}{}
			observation, exists := observations[observationID]
			if !exists {
				return fmt.Errorf("affine segment %d references unknown observation_id %q", index, observationID)
			}
			unwrapped, err := UnwrapTicks(clock, observation.Tick, observation.WrapCount)
			if err != nil {
				return fmt.Errorf("affine segment %d observation %q: %w", index, observationID, err)
			}
			if unwrapped < segment.StartUnwrappedTick || unwrapped >= segment.EndUnwrappedTick {
				return fmt.Errorf("affine segment %d observation %q is outside its tick range", index, observationID)
			}
			if !observationIntervalCovered(*segment, observation, unwrapped) {
				return fmt.Errorf(
					"affine segment %d uncertainty does not cover observation %q host interval",
					index,
					observationID,
				)
			}
		}
		if previous != nil {
			if segment.StartUnwrappedTick < previous.EndUnwrappedTick {
				return fmt.Errorf("affine segment %d overlaps its predecessor", index)
			}
			previousEnd := mappedHostTime(*previous, previous.EndUnwrappedTick)
			currentStart := mappedHostTime(*segment, segment.StartUnwrappedTick)
			if previousEnd.Cmp(currentStart) > 0 {
				return fmt.Errorf("affine segment %d moves nominal host time backwards", index)
			}
		}
		if err := validateMappedRange(*segment); err != nil {
			return fmt.Errorf("affine segment %d: %w", index, err)
		}
		previous = segment
	}
	return nil
}

func observationIntervalCovered(
	segment AffineSegment,
	observation ClockObservation,
	unwrapped uint64,
) bool {
	nominal := mappedHostTime(segment, unwrapped)
	uncertainty := new(big.Rat).SetInt(new(big.Int).SetUint64(segment.UncertaintyNS))
	lower := new(big.Rat).Sub(nominal, uncertainty)
	upper := new(big.Rat).Add(nominal, uncertainty)
	before := new(big.Rat).SetInt(new(big.Int).SetUint64(observation.HostBeforeNS))
	after := new(big.Rat).SetInt(new(big.Int).SetUint64(observation.HostAfterNS))
	return lower.Cmp(before) <= 0 && upper.Cmp(after) >= 0
}

func validateMappedRange(segment AffineSegment) error {
	start := mappedHostTime(segment, segment.StartUnwrappedTick)
	end := mappedHostTime(segment, segment.EndUnwrappedTick)
	zero := new(big.Rat)
	maximum := new(big.Rat).SetInt(new(big.Int).SetUint64(math.MaxUint64))
	uncertainty := new(big.Rat).SetInt(new(big.Int).SetUint64(segment.UncertaintyNS))
	lower := new(big.Rat).Sub(start, uncertainty)
	upper := new(big.Rat).Add(end, uncertainty)
	if lower.Cmp(zero) < 0 || upper.Cmp(maximum) > 0 {
		return errors.New("mapped interval plus uncertainty leaves the uint64 nanosecond domain")
	}
	return nil
}

func mappedHostTime(segment AffineSegment, tick uint64) *big.Rat {
	delta := new(big.Int).Sub(
		new(big.Int).SetUint64(tick),
		new(big.Int).SetUint64(segment.SourceOriginTick),
	)
	delta.Mul(delta, new(big.Int).SetUint64(segment.ScaleNum))
	mapped := new(big.Rat).SetFrac(delta, new(big.Int).SetUint64(segment.ScaleDen))
	mapped.Add(mapped, new(big.Rat).SetInt(new(big.Int).SetUint64(segment.HostOriginNS)))
	return mapped
}

func validateArtifacts(source Source) error {
	paths := make(map[string]struct{}, len(source.Artifacts))
	var payload, index *Artifact
	for artifactIndex := range source.Artifacts {
		artifact := &source.Artifacts[artifactIndex]
		if err := validateArtifactLeaf("artifact.path", artifact.Path); err != nil {
			return fmt.Errorf("artifact %d: %w", artifactIndex, err)
		}
		if _, duplicate := paths[artifact.Path]; duplicate {
			return fmt.Errorf("duplicate artifact path %q", artifact.Path)
		}
		paths[artifact.Path] = struct{}{}
		if !sha256Pattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("artifact %q sha256 must be 64 lowercase hexadecimal digits", artifact.Path)
		}
		switch artifact.Role {
		case ArtifactPayload:
			if payload != nil {
				return errors.New("complete source must declare exactly one payload artifact")
			}
			payload = artifact
		case ArtifactIndex:
			if index != nil {
				return errors.New("complete source must declare exactly one index artifact")
			}
			index = artifact
		case ArtifactConfiguration, ArtifactManifest, ArtifactMetadata:
		default:
			return fmt.Errorf("artifact %q has unsupported role %q", artifact.Path, artifact.Role)
		}
	}
	if source.Outcome != OutcomeComplete {
		if len(source.Artifacts) != 0 {
			return errors.New("failed or omitted source must not publish artifacts")
		}
		return nil
	}
	if payload == nil || index == nil {
		return errors.New("complete source requires exactly one payload and one index artifact")
	}
	if payload.Path != source.Payload.Filename || payload.SizeBytes != source.PayloadBytes {
		return errors.New("payload artifact path/size disagrees with the source payload contract")
	}
	expectedIndexBytes, err := encodedIndexSize(source.ItemCount)
	if err != nil {
		return err
	}
	if index.Path != "index.bin" || index.SizeBytes != expectedIndexBytes {
		return errors.New("index artifact must be index.bin with exact 32+64*item_count size")
	}
	return nil
}

func validateSyncEvents(events []SyncEvent, domains map[string]*Source) error {
	seen := make(map[uint64]struct{}, len(events))
	var previousID uint64
	for index, event := range events {
		if event.SyncEventID == NoSyncEventID {
			return fmt.Errorf("sync event %d uses reserved MAX_U64 id", index)
		}
		if _, duplicate := seen[event.SyncEventID]; duplicate {
			return fmt.Errorf("duplicate sync_event_id %d", event.SyncEventID)
		}
		if index > 0 && event.SyncEventID <= previousID {
			return errors.New("sync_events must be ordered by strictly increasing sync_event_id")
		}
		previousID = event.SyncEventID
		seen[event.SyncEventID] = struct{}{}
		source, exists := domains[event.ClockID]
		if !exists {
			return fmt.Errorf("sync event %d references unknown clock_id %q", event.SyncEventID, event.ClockID)
		}
		if source == nil {
			return fmt.Errorf("sync event %d cannot use the host clock without source observations", event.SyncEventID)
		}
		unwrapped, err := UnwrapTicks(source.Clock, event.Tick, event.WrapCount)
		if err != nil {
			return fmt.Errorf("sync event %d: %w", event.SyncEventID, err)
		}
		if segmentCoverage(source.AffineSegments, unwrapped) != 1 {
			return fmt.Errorf("sync event %d tick is not covered by exactly one affine segment", event.SyncEventID)
		}
		switch event.Edge {
		case EventRising, EventFalling:
		default:
			return fmt.Errorf("sync event %d has unsupported edge %q", event.SyncEventID, event.Edge)
		}
		switch event.EvidenceKind {
		case EvidenceTriggerGeneration, EvidenceHardwareObservation:
		default:
			return fmt.Errorf(
				"sync event %d has unsupported evidence_kind %q",
				event.SyncEventID,
				event.EvidenceKind,
			)
		}
		for label, value := range map[string]string{
			"generator":  event.Generator,
			"observer":   event.Observer,
			"routing_id": event.RoutingID,
		} {
			if err := validateOpaqueID(label, value); err != nil {
				return fmt.Errorf("sync event %d: %w", event.SyncEventID, err)
			}
		}
		if event.ObservationIDs == nil || len(event.ObservationIDs) == 0 {
			return fmt.Errorf("sync event %d has no supporting observation_ids", event.SyncEventID)
		}
		available := make(map[string]struct{}, len(source.ClockObservations))
		for _, observation := range source.ClockObservations {
			available[observation.ObservationID] = struct{}{}
		}
		eventObservations := make(map[string]struct{}, len(event.ObservationIDs))
		for _, observationID := range event.ObservationIDs {
			if _, duplicate := eventObservations[observationID]; duplicate {
				return fmt.Errorf("sync event %d repeats observation_id %q", event.SyncEventID, observationID)
			}
			eventObservations[observationID] = struct{}{}
			if _, exists := available[observationID]; !exists {
				return fmt.Errorf("sync event %d references unknown observation_id %q", event.SyncEventID, observationID)
			}
		}
	}
	return nil
}

func validateSourceIndex(source Source, index SensorIndex, events map[uint64]SyncEvent) error {
	if err := index.Validate(source.Limits); err != nil {
		return err
	}
	if uint64(len(index.Entries)) != source.ItemCount || index.PayloadBytes != source.PayloadBytes {
		return errors.New("sensor index header disagrees with source item_count/payload_bytes")
	}
	encoded, err := EncodeSensorIndex(index, source.Limits)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	indexArtifact, err := artifactByRole(source.Artifacts, ArtifactIndex)
	if err != nil {
		return err
	}
	if indexArtifact.SHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("index artifact sha256 disagrees with encoded sensor index")
	}

	eventCounts := make(map[uint64]uint64)
	var previousTick uint64
	for item, entry := range index.Entries {
		unwrapped, err := UnwrapTicks(source.Clock, entry.Tick, entry.WrapCount)
		if err != nil {
			return fmt.Errorf("entry %d clock: %w", item, err)
		}
		if item > 0 && unwrapped < previousTick {
			return fmt.Errorf("entry %d unwrapped tick moves backwards", item)
		}
		previousTick = unwrapped
		if _, ok := checkedAddU64(unwrapped, entry.DurationTicks); !ok {
			return fmt.Errorf("entry %d duration overflows the unwrapped clock", item)
		}
		if segmentCoverage(source.AffineSegments, unwrapped) != 1 {
			return fmt.Errorf("entry %d tick is not covered by exactly one affine segment", item)
		}
		if entry.SyncEventID == NoSyncEventID {
			if source.SyncEventCardinality != nil && source.SyncEventCardinality.Required {
				return fmt.Errorf("entry %d omits a required sync event", item)
			}
			continue
		}
		if source.SyncEventCardinality == nil {
			return fmt.Errorf("entry %d declares a sync event without sync_event_cardinality", item)
		}
		if _, exists := events[entry.SyncEventID]; !exists {
			return fmt.Errorf("entry %d references unknown sync_event_id %d", item, entry.SyncEventID)
		}
		count, ok := checkedAddU64(eventCounts[entry.SyncEventID], 1)
		if !ok {
			return fmt.Errorf("sync_event_id %d cardinality overflows uint64", entry.SyncEventID)
		}
		eventCounts[entry.SyncEventID] = count
	}
	if source.SyncEventCardinality == nil {
		return nil
	}
	cardinality := *source.SyncEventCardinality
	for eventID, count := range eventCounts {
		if count < cardinality.MinItems || count > cardinality.MaxItems {
			return fmt.Errorf(
				"sync_event_id %d has %d items outside declared cardinality %d..%d",
				eventID,
				count,
				cardinality.MinItems,
				cardinality.MaxItems,
			)
		}
	}
	if cardinality.Required {
		for eventID := range events {
			count := eventCounts[eventID]
			if count < cardinality.MinItems || count > cardinality.MaxItems {
				return fmt.Errorf(
					"required sync_event_id %d has %d items outside declared cardinality %d..%d",
					eventID,
					count,
					cardinality.MinItems,
					cardinality.MaxItems,
				)
			}
		}
	}
	return nil
}

func artifactByRole(artifacts []Artifact, role ArtifactRole) (Artifact, error) {
	for _, artifact := range artifacts {
		if artifact.Role == role {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("missing artifact role %q", role)
}

func segmentCoverage(segments []AffineSegment, tick uint64) int {
	coverage := 0
	for _, segment := range segments {
		if tick >= segment.StartUnwrappedTick && tick < segment.EndUnwrappedTick {
			coverage++
		}
	}
	return coverage
}

func validateApplicationMetadata(label string, metadata ApplicationMetadata) error {
	if metadata == nil {
		return fmt.Errorf("%s application_metadata must be an object", label)
	}
	if len(metadata) > MaximumApplicationMetadataEntries {
		return fmt.Errorf(
			"%s application_metadata has %d keys, maximum is %d",
			label,
			len(metadata),
			MaximumApplicationMetadataEntries,
		)
	}
	for key, value := range metadata {
		if len(key) > MaximumApplicationMetadataKeyBytes || !metadataKeyPattern.MatchString(key) {
			return fmt.Errorf("%s application_metadata key %q is not namespaced", label, key)
		}
		if err := validateJSONDocument(value, MaximumApplicationMetadataDepth); err != nil {
			return fmt.Errorf("%s application_metadata[%q]: %w", label, key, err)
		}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode %s application_metadata: %w", label, err)
	}
	if len(encoded) > MaximumApplicationMetadataBytes {
		return fmt.Errorf(
			"%s application_metadata is %d bytes, maximum is %d",
			label,
			len(encoded),
			MaximumApplicationMetadataBytes,
		)
	}
	return nil
}

func validateArtifactLeaf(label, path string) error {
	if path == "" || len(path) > MaximumArtifactPathBytes || path == "." || path == ".." ||
		filepath.Base(path) != path || strings.ContainsAny(path, `/\\`) ||
		strings.HasSuffix(path, ".part") || strings.IndexByte(path, 0) >= 0 {
		return fmt.Errorf("%s %q is not a safe fixed leaf", label, path)
	}
	return nil
}

func validateOpaqueID(label, value string) error {
	if len(value) > MaximumOpaqueIDBytes || !opaqueIDPattern.MatchString(value) {
		return fmt.Errorf("%s %q is not a valid opaque identifier", label, value)
	}
	return nil
}

func validateJSONDocument(encoded []byte, maximumDepth int) error {
	if len(encoded) == 0 {
		return errors.New("JSON value is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, 1, maximumDepth); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return fmt.Errorf("trailing JSON token %v", token)
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, depth, maximumDepth int) error {
	if depth > maximumDepth {
		return fmt.Errorf("JSON nesting exceeds depth %d", maximumDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object has an invalid closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array has an invalid closing delimiter")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing multisensor session JSON: %w", err)
		}
		return errors.New("multisensor session JSON contains a trailing value")
	}
	return nil
}

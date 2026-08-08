package multisensorstream

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"mmwcli/internal/multisensor"
)

const (
	SchemaV1            = "mmwcli.multisensor_stream.v1"
	RadarConfigSchemaV1 = "mmwcli.multisensor_stream_radar_config.v1"
	ItemSchemaV1        = "mmwcli.multisensor_stream_item.v1"
	EndSchemaV1         = "mmwcli.multisensor_stream_end.v1"
	TerminalSchemaV1    = "mmwcli.multisensor_stream_terminal.v1"
	EOFSchemaV1         = "mmwcli.multisensor_stream_eof.v1"
)

type SourceKind = multisensor.SourceKind
type TimestampSemantics = multisensor.TimestampSemantics
type SynchronizationGrade = multisensor.SynchronizationGrade
type SourceOutcome = multisensor.SourceOutcome
type SourceLimits = multisensor.SourceLimits
type PayloadContract = multisensor.PayloadContract
type Clock = multisensor.Clock

const (
	SourceRadar                    = multisensor.SourceRadar
	SourceCamera                   = multisensor.SourceCamera
	TimestampFrameStart            = multisensor.TimestampFrameStart
	TimestampExposureMidpoint      = multisensor.TimestampExposureMidpoint
	SynchronizationSoftwareBarrier = multisensor.SynchronizationSoftwareBarrier
	OutcomeComplete                = multisensor.OutcomeComplete
	OutcomeFailed                  = multisensor.OutcomeFailed
	OutcomeOmitted                 = multisensor.OutcomeOmitted
	NoSyncEventID                  = multisensor.NoSyncEventID
)

type Source struct {
	SourceID string          `json:"source_id"`
	Kind     SourceKind      `json:"kind"`
	Required bool            `json:"required"`
	Payload  PayloadContract `json:"payload"`
	Clock    Clock           `json:"clock"`
	Limits   SourceLimits    `json:"limits"`
}

type Session struct {
	SessionID            string
	SynchronizationGrade SynchronizationGrade
	Sources              []Source
}

type Item struct {
	SourceID      string
	ItemIndex     uint64
	Tick          uint64
	WrapCount     uint64
	DurationTicks uint64
	SyncEventID   uint64
	Payload       []byte
}

type SessionArtifact struct {
	SizeBytes uint64
	SHA256    [32]byte
}

type AbortReason string

const (
	AbortCancelled       AbortReason = "cancelled"
	AbortBackpressure    AbortReason = "backpressure"
	AbortSourceFailed    AbortReason = "source_failed"
	AbortIntegrityFailed AbortReason = "integrity_failed"
	AbortCleanupFailed   AbortReason = "cleanup_failed"
	AbortPublishFailed   AbortReason = "publish_failed"
)

type sessionRecordV1 struct {
	Schema               string               `json:"schema"`
	SessionID            string               `json:"session_id"`
	SynchronizationGrade SynchronizationGrade `json:"synchronization_grade"`
	Sources              []Source             `json:"sources"`
}

type radarConfigRecordV1 struct {
	Schema    string `json:"schema"`
	SourceID  string `json:"source_id"`
	Format    string `json:"format"`
	SizeBytes uint64 `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type itemRecordV1 struct {
	Schema        string `json:"schema"`
	SourceID      string `json:"source_id"`
	ItemIndex     uint64 `json:"item_index"`
	Provisional   bool   `json:"provisional"`
	Tick          uint64 `json:"tick"`
	WrapCount     uint64 `json:"wrap_count"`
	DurationTicks uint64 `json:"duration_ticks"`
	SyncEventID   uint64 `json:"sync_event_id"`
}

type endRecordV1 struct {
	Schema        string        `json:"schema"`
	SourceID      string        `json:"source_id"`
	Outcome       SourceOutcome `json:"outcome"`
	ItemCount     uint64        `json:"item_count"`
	PayloadBytes  uint64        `json:"payload_bytes"`
	PayloadSHA256 string        `json:"payload_sha256"`
}

type commitRecordV1 struct {
	Schema            string `json:"schema"`
	SessionID         string `json:"session_id"`
	Outcome           string `json:"outcome"`
	SessionJSONBytes  uint64 `json:"session_json_size_bytes"`
	SessionJSONSHA256 string `json:"session_json_sha256"`
}

type abortRecordV1 struct {
	Schema     string      `json:"schema"`
	SessionID  string      `json:"session_id"`
	Outcome    string      `json:"outcome"`
	ReasonCode AbortReason `json:"reason_code"`
}

type eofRecordV1 struct {
	Schema    string `json:"schema"`
	SessionID string `json:"session_id"`
}

var (
	sessionIDPattern = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
	sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	formatPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

func validateSession(session Session) error {
	if !sessionIDPattern.MatchString(session.SessionID) {
		return errors.New("session_id must be a lowercase UUIDv4")
	}
	if session.SynchronizationGrade != SynchronizationSoftwareBarrier {
		return errors.New("multisensor stream v1 requires software_barrier synchronization")
	}
	if len(session.Sources) == 0 || len(session.Sources) > multisensor.MaximumSources {
		return fmt.Errorf("sources must contain 1..%d entries", multisensor.MaximumSources)
	}
	identifiers := make(map[string]struct{}, len(session.Sources))
	clockIDs := make(map[string]struct{}, len(session.Sources))
	for index, source := range session.Sources {
		if err := validateSource(source); err != nil {
			return fmt.Errorf("source %d: %w", index, err)
		}
		if _, duplicate := identifiers[source.SourceID]; duplicate {
			return fmt.Errorf("duplicate source_id %q", source.SourceID)
		}
		identifiers[source.SourceID] = struct{}{}
		if _, duplicate := clockIDs[source.Clock.ClockID]; duplicate {
			return fmt.Errorf("duplicate source clock_id %q", source.Clock.ClockID)
		}
		clockIDs[source.Clock.ClockID] = struct{}{}
	}
	return nil
}

func validateSource(source Source) error {
	if !sourceIDPattern.MatchString(source.SourceID) {
		return fmt.Errorf("source_id %q is invalid", source.SourceID)
	}
	if err := source.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if err := validateLeaf(source.Payload.Filename); err != nil {
		return fmt.Errorf("payload.filename: %w", err)
	}
	if source.Payload.Filename == "index.bin" {
		return errors.New("payload.filename cannot be index.bin")
	}
	if !formatPattern.MatchString(source.Payload.Format) {
		return fmt.Errorf("payload.format %q is invalid", source.Payload.Format)
	}
	if !opaqueIDPattern.MatchString(source.Clock.ClockID) {
		return fmt.Errorf("clock_id %q is invalid", source.Clock.ClockID)
	}
	if source.Clock.TickHz == 0 || source.Clock.WrapTicks == 1 {
		return errors.New("clock tick_hz must be positive and wrap_ticks must be zero or greater than one")
	}
	switch source.Kind {
	case SourceRadar:
		if source.Clock.TimestampSemantics != TimestampFrameStart {
			return errors.New("radar clock must use frame_start semantics")
		}
	case SourceCamera:
		if source.Clock.TimestampSemantics != TimestampExposureMidpoint {
			return errors.New("camera clock must use exposure_midpoint semantics")
		}
	default:
		return fmt.Errorf("unsupported source kind %q", source.Kind)
	}
	return nil
}

func validateLeaf(value string) error {
	if len(value) == 0 || len(value) > multisensor.MaximumArtifactPathBytes || !utf8.ValidString(value) {
		return fmt.Errorf("must contain 1..%d UTF-8 bytes", multisensor.MaximumArtifactPathBytes)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, `/\\\x00`) || strings.HasSuffix(value, ".part") {
		return errors.New("must be a single published leaf")
	}
	return nil
}

func validateAbortReason(reason AbortReason) error {
	switch reason {
	case AbortCancelled, AbortBackpressure, AbortSourceFailed, AbortIntegrityFailed,
		AbortCleanupFailed, AbortPublishFailed:
		return nil
	default:
		return fmt.Errorf("unsupported multisensor stream abort reason %q", reason)
	}
}

func validateSourceOutcome(outcome SourceOutcome) error {
	switch outcome {
	case OutcomeComplete, OutcomeFailed, OutcomeOmitted:
		return nil
	default:
		return fmt.Errorf("unsupported multisensor stream source outcome %q", outcome)
	}
}

func validateArtifact(artifact SessionArtifact) error {
	if artifact.SizeBytes == 0 || artifact.SizeBytes > multisensor.MaximumSessionJSONBytes {
		return fmt.Errorf(
			"published session.json size %d is outside 1..%d",
			artifact.SizeBytes,
			multisensor.MaximumSessionJSONBytes,
		)
	}
	return nil
}

func digestString(digest [32]byte) string {
	return hex.EncodeToString(digest[:])
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func checkedItemTime(clock Clock, tick, wrapCount, duration uint64) error {
	unwrapped, err := multisensor.UnwrapTicks(clock, tick, wrapCount)
	if err != nil {
		return err
	}
	if duration > math.MaxUint64-unwrapped {
		return errors.New("duration overflows the unwrapped source clock")
	}
	return nil
}

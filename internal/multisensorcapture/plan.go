package multisensorcapture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/sensorproducer"
)

const (
	PlanSchema                  = "mmwcli.multisensor_plan.v1"
	ProducerSessionSchema       = "mmwcli.sensor_producer_session.v1"
	ProducerItemSchema          = "mmwcli.sensor_producer_item.v1"
	ProducerEndSchema           = "mmwcli.sensor_producer_end.v1"
	ProducerEOFSchema           = "mmwcli.sensor_producer_eof.v1"
	SyncEventSemanticsNone      = "none"
	MaximumPlanBytes            = 1 << 20
	maximumPlanDepth            = 32
	maximumCommandArguments     = 64
	maximumCommandArgumentBytes = 4096
	maximumCommandTotalBytes    = 64 << 10
	validationSessionID         = "123e4567-e89b-42d3-a456-426614174000"
)

type Plan struct {
	Schema              string                          `json:"schema"`
	Sources             []SourcePlan                    `json:"sources"`
	ApplicationMetadata multisensor.ApplicationMetadata `json:"application_metadata"`
}

// SourcePlan declares one external source. Radar is deliberately absent: the
// app later combines these results with its one authoritative radar route.
type SourcePlan struct {
	SourceID            string                          `json:"source_id"`
	Kind                multisensor.SourceKind          `json:"kind"`
	Required            bool                            `json:"required"`
	Argv                []string                        `json:"argv"`
	QueueSize           int                             `json:"queue_size"`
	Producer            multisensor.Producer            `json:"producer"`
	Limits              multisensor.SourceLimits        `json:"limits"`
	Payload             multisensor.PayloadContract     `json:"payload"`
	Clock               multisensor.Clock               `json:"clock"`
	SyncEventSemantics  string                          `json:"sync_event_semantics"`
	ApplicationMetadata multisensor.ApplicationMetadata `json:"application_metadata"`
}

type ProducerSessionMetadata struct {
	Schema              string                          `json:"schema"`
	Kind                multisensor.SourceKind          `json:"kind"`
	Producer            multisensor.Producer            `json:"producer"`
	Limits              multisensor.SourceLimits        `json:"limits"`
	Payload             multisensor.PayloadContract     `json:"payload"`
	Clock               multisensor.Clock               `json:"clock"`
	ClockObservations   []multisensor.ClockObservation  `json:"clock_observations"`
	AffineSegments      []multisensor.AffineSegment     `json:"affine_segments"`
	SyncEventSemantics  string                          `json:"sync_event_semantics"`
	ApplicationMetadata multisensor.ApplicationMetadata `json:"application_metadata"`
}

type ProducerItemMetadata struct {
	Schema        string `json:"schema"`
	ItemIndex     uint64 `json:"item_index"`
	Tick          uint64 `json:"tick"`
	WrapCount     uint64 `json:"wrap_count"`
	DurationTicks uint64 `json:"duration_ticks"`
	SyncEventID   uint64 `json:"sync_event_id"`
}

type ProducerEndMetadata struct {
	Schema        string `json:"schema"`
	ItemCount     uint64 `json:"item_count"`
	PayloadBytes  uint64 `json:"payload_bytes"`
	PayloadSHA256 string `json:"payload_sha256"`
}

type ProducerEOFMetadata struct {
	Schema string `json:"schema"`
}

func LoadPlan(path string) (Plan, error) {
	file, err := os.Open(path)
	if err != nil {
		return Plan{}, fmt.Errorf("open multisensor plan %s: %w", path, err)
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, MaximumPlanBytes+1))
	if err != nil {
		return Plan{}, fmt.Errorf("read multisensor plan %s: %w", path, err)
	}
	if len(encoded) > MaximumPlanBytes {
		return Plan{}, fmt.Errorf("multisensor plan exceeds %d bytes", MaximumPlanBytes)
	}
	return ParsePlan(encoded)
}

func ParsePlan(encoded []byte) (Plan, error) {
	if len(encoded) == 0 || len(encoded) > MaximumPlanBytes {
		return Plan{}, fmt.Errorf("multisensor plan size %d is outside 1..%d", len(encoded), MaximumPlanBytes)
	}
	if err := rejectDuplicateJSONKeys(encoded, maximumPlanDepth); err != nil {
		return Plan{}, fmt.Errorf("invalid multisensor plan JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode multisensor plan: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Plan{}, err
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (plan Plan) Validate() error {
	if plan.Schema != PlanSchema {
		return fmt.Errorf("multisensor plan schema is %q, want %q", plan.Schema, PlanSchema)
	}
	if plan.Sources == nil || len(plan.Sources) == 0 || len(plan.Sources) >= multisensor.MaximumSources {
		return fmt.Errorf(
			"external sources count %d is outside 1..%d; one aggregate slot is reserved for radar",
			len(plan.Sources),
			multisensor.MaximumSources-1,
		)
	}
	sources := make([]multisensor.Source, 0, len(plan.Sources))
	indexes := make(map[string]multisensor.SensorIndex, len(plan.Sources))
	required := uint64(0)
	for index, sourcePlan := range plan.Sources {
		if sourcePlan.Kind != multisensor.SourceCamera {
			return fmt.Errorf(
				"source %d kind %q is invalid: radar is implicit and v1 external sources are camera only",
				index,
				sourcePlan.Kind,
			)
		}
		if err := validateCommand(sourcePlan.Argv); err != nil {
			return fmt.Errorf("source %q: %w", sourcePlan.SourceID, err)
		}
		if sourcePlan.QueueSize < 0 || sourcePlan.QueueSize > sensorproducer.MaxQueueSize {
			return fmt.Errorf(
				"source %q queue_size must be in 0..%d",
				sourcePlan.SourceID,
				sensorproducer.MaxQueueSize,
			)
		}
		if sourcePlan.SyncEventSemantics != SyncEventSemanticsNone {
			return fmt.Errorf(
				"source %q sync_event_semantics must be %q for software_barrier v1",
				sourcePlan.SourceID,
				SyncEventSemanticsNone,
			)
		}
		metadata := ProducerSessionMetadata{
			Schema: ProducerSessionSchema, Kind: sourcePlan.Kind, Producer: sourcePlan.Producer,
			Limits: sourcePlan.Limits, Payload: sourcePlan.Payload, Clock: sourcePlan.Clock,
			ClockObservations:   []multisensor.ClockObservation{},
			AffineSegments:      []multisensor.AffineSegment{},
			SyncEventSemantics:  sourcePlan.SyncEventSemantics,
			ApplicationMetadata: sourcePlan.ApplicationMetadata,
		}
		source, sensorIndex, err := emptyCompleteSource(sourcePlan, metadata)
		if err != nil {
			return fmt.Errorf("source %q: %w", sourcePlan.SourceID, err)
		}
		sources = append(sources, source)
		indexes[source.SourceID] = sensorIndex
		if source.Required {
			required++
		}
	}
	session := validationSession(plan.ApplicationMetadata, sources, multisensor.AggregateTotals{
		SourceCount: uint64(len(sources)), RequiredSourceCount: required,
		CompleteSourceCount: uint64(len(sources)),
	})
	if err := session.ValidateWithIndexes(indexes); err != nil {
		return fmt.Errorf("multisensor plan source contract: %w", err)
	}
	return nil
}

func validateProducerSessionMetadata(plan SourcePlan, metadata ProducerSessionMetadata) error {
	if metadata.Schema != ProducerSessionSchema {
		return fmt.Errorf("producer SESSION schema is %q, want %q", metadata.Schema, ProducerSessionSchema)
	}
	if metadata.Kind != plan.Kind || metadata.Producer != plan.Producer ||
		metadata.Limits != plan.Limits || metadata.Payload != plan.Payload || metadata.Clock != plan.Clock ||
		metadata.SyncEventSemantics != plan.SyncEventSemantics {
		return errors.New("producer SESSION static contract does not match the plan")
	}
	if !equalMetadata(metadata.ApplicationMetadata, plan.ApplicationMetadata) {
		return errors.New("producer SESSION application_metadata does not match the plan")
	}
	if metadata.SyncEventSemantics != SyncEventSemanticsNone {
		return errors.New("software_barrier producer SESSION must declare sync_event_semantics=none")
	}
	_, _, err := emptyCompleteSource(plan, metadata)
	if err != nil {
		return err
	}
	return nil
}

func emptyCompleteSource(
	plan SourcePlan,
	metadata ProducerSessionMetadata,
) (multisensor.Source, multisensor.SensorIndex, error) {
	index := multisensor.SensorIndex{Entries: []multisensor.IndexEntry{}}
	indexBytes, err := multisensor.EncodeSensorIndex(index, metadata.Limits)
	if err != nil {
		return multisensor.Source{}, index, err
	}
	emptyDigest := sha256.Sum256(nil)
	indexDigest := sha256.Sum256(indexBytes)
	source := multisensor.Source{
		SourceID: plan.SourceID, Kind: metadata.Kind, Required: plan.Required,
		Outcome: multisensor.OutcomeComplete, Producer: metadata.Producer, Limits: metadata.Limits,
		Payload: metadata.Payload, Clock: metadata.Clock,
		ClockObservations: metadata.ClockObservations, AffineSegments: metadata.AffineSegments,
		Artifacts: []multisensor.Artifact{
			{
				Role: multisensor.ArtifactPayload, Path: metadata.Payload.Filename,
				SHA256: hex.EncodeToString(emptyDigest[:]),
			},
			{
				Role: multisensor.ArtifactIndex, Path: multisensor.IndexFileName,
				SizeBytes: uint64(len(indexBytes)), SHA256: hex.EncodeToString(indexDigest[:]),
			},
		},
		ApplicationMetadata: metadata.ApplicationMetadata,
	}
	if err := validateOneSource(source, index); err != nil {
		return multisensor.Source{}, index, err
	}
	return source, index, nil
}

func validateOneSource(source multisensor.Source, index multisensor.SensorIndex) error {
	required := uint64(0)
	if source.Required {
		required = 1
	}
	session := validationSession(multisensor.ApplicationMetadata{}, []multisensor.Source{source}, multisensor.AggregateTotals{
		SourceCount: 1, RequiredSourceCount: required, CompleteSourceCount: 1,
		ItemCount: source.ItemCount, PayloadBytes: source.PayloadBytes,
	})
	return session.ValidateWithIndexes(map[string]multisensor.SensorIndex{source.SourceID: index})
}

func validationSession(
	metadata multisensor.ApplicationMetadata,
	sources []multisensor.Source,
	totals multisensor.AggregateTotals,
) multisensor.Session {
	return multisensor.Session{
		Schema: multisensor.SessionSchema, SessionID: validationSessionID,
		SynchronizationGrade: multisensor.SynchronizationSoftwareBarrier,
		HostClock: multisensor.Clock{
			ClockID: "host-monotonic", TickHz: 1_000_000_000,
			TimestampSemantics: multisensor.TimestampHostMonotonic,
		},
		Sources: sources, SyncEvents: []multisensor.SyncEvent{}, Totals: totals,
		ApplicationMetadata: metadata,
	}
}

func validateCommand(argv []string) error {
	if argv == nil || len(argv) == 0 || len(argv) > maximumCommandArguments {
		return fmt.Errorf("argv count must be in 1..%d", maximumCommandArguments)
	}
	total := 0
	for index, argument := range argv {
		if (index == 0 && argument == "") || len(argument) > maximumCommandArgumentBytes ||
			strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("argv[%d] is empty, too large, or contains NUL", index)
		}
		total += len(argument)
		if total > maximumCommandTotalBytes {
			return fmt.Errorf("argv exceeds %d total bytes", maximumCommandTotalBytes)
		}
	}
	return nil
}

func equalMetadata(left, right multisensor.ApplicationMetadata) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func decodeMetadata(encoded []byte, target any) error {
	if err := rejectDuplicateJSONKeys(encoded, maximumPlanDepth); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEnd(decoder)
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains a trailing value")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(encoded []byte, maximumDepth int) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := walkJSON(decoder, 1, maximumDepth); err != nil {
		return err
	}
	return requireJSONEnd(decoder)
}

func walkJSON(decoder *json.Decoder, depth, maximumDepth int) error {
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
			if err := walkJSON(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1, maximumDepth); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

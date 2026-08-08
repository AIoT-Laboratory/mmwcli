package multisensorstream

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
)

type encoderState uint8

const (
	encoderActive encoderState = iota
	encoderPoisoned
	encoderTerminal
)

type sourceProgress struct {
	contract     Source
	nextItem     uint64
	payloadBytes uint64
	payloadHash  hash.Hash
	radarStarted bool
	ended        bool
	outcome      SourceOutcome
}

// Encoder writes one finite aggregate stream. It is a single-owner state
// machine; records from different sources may be interleaved explicitly.
type Encoder struct {
	writer         io.Writer
	sessionID      string
	sources        map[string]*sourceProgress
	nextRecord     uint64
	configWritten  bool
	recordsStarted bool
	endingStarted  bool
	state          encoderState
	failure        error
}

// NewEncoder validates the complete static source contract and emits SESSION
// as record_seq zero.
func NewEncoder(writer io.Writer, session Session) (*Encoder, error) {
	if writer == nil {
		return nil, errors.New("multisensor stream writer is nil")
	}
	if err := validateSession(session); err != nil {
		return nil, fmt.Errorf("validate multisensor stream session: %w", err)
	}
	sources := make(map[string]*sourceProgress, len(session.Sources))
	wireSources := append([]Source(nil), session.Sources...)
	for _, source := range wireSources {
		sources[source.SourceID] = &sourceProgress{contract: source, payloadHash: sha256.New()}
	}
	metadata, err := encodeMetadata(sessionRecordV1{
		Schema:               SchemaV1,
		SessionID:            session.SessionID,
		SynchronizationGrade: session.SynchronizationGrade,
		Sources:              wireSources,
	})
	if err != nil {
		return nil, err
	}
	if err := writeRecord(writer, Record{Type: RecordSession, RecordSeq: 0, Metadata: metadata}); err != nil {
		return nil, fmt.Errorf("emit multisensor stream SESSION: %w", err)
	}
	return &Encoder{
		writer:     writer,
		sessionID:  session.SessionID,
		sources:    sources,
		nextRecord: 1,
		state:      encoderActive,
	}, nil
}

// WriteRadarConfig emits the optional single raw radar configuration before
// any ITEM or END record.
func (encoder *Encoder) WriteRadarConfig(sourceID, format string, payload []byte) error {
	if err := encoder.requireActive(); err != nil {
		return err
	}
	if encoder.configWritten || encoder.recordsStarted {
		return encoder.poison(errors.New("RADAR_CONFIG must be unique and precede ITEM/END records"))
	}
	source, exists := encoder.sources[sourceID]
	if !exists || source.contract.Kind != SourceRadar {
		return encoder.poison(fmt.Errorf("RADAR_CONFIG source %q is not a declared radar", sourceID))
	}
	if !formatPattern.MatchString(format) {
		return encoder.poison(fmt.Errorf("RADAR_CONFIG format %q is invalid", format))
	}
	if len(payload) == 0 || len(payload) > MaximumRadarConfigBytes {
		return encoder.poison(fmt.Errorf("RADAR_CONFIG payload size %d is invalid", len(payload)))
	}
	digest := sha256.Sum256(payload)
	metadata, err := encodeMetadata(radarConfigRecordV1{
		Schema: RadarConfigSchemaV1, SourceID: sourceID, Format: format,
		SizeBytes: uint64(len(payload)), SHA256: digestString(digest),
	})
	if err != nil {
		return encoder.poison(err)
	}
	if err := encoder.emit(RecordRadarConfig, metadata, payload); err != nil {
		return encoder.poison(err)
	}
	encoder.configWritten = true
	return nil
}

// WriteRadarStart emits the one conservative host-monotonic bound for a
// declared radar's tick-zero origin. It must precede that radar's first ITEM.
func (encoder *Encoder) WriteRadarStart(start RadarStart) error {
	if err := encoder.requireActive(); err != nil {
		return err
	}
	source, exists := encoder.sources[start.SourceID]
	if !exists || source.contract.Kind != SourceRadar {
		return encoder.poison(fmt.Errorf("RADAR_START source %q is not a declared radar", start.SourceID))
	}
	if source.radarStarted {
		return encoder.poison(fmt.Errorf("radar source %q already emitted RADAR_START", start.SourceID))
	}
	if source.nextItem != 0 || source.ended || encoder.endingStarted {
		return encoder.poison(fmt.Errorf("RADAR_START for %q must precede its first ITEM and every END", start.SourceID))
	}
	if start.HostLowerNS > start.HostUpperNS {
		return encoder.poison(errors.New("RADAR_START host_lower_ns must not exceed host_upper_ns"))
	}
	metadata, err := encodeMetadata(radarStartRecordV1{
		Schema: RadarStartSchemaV1, SourceID: start.SourceID,
		HostLowerNS: start.HostLowerNS, HostUpperNS: start.HostUpperNS,
	})
	if err != nil {
		return encoder.poison(err)
	}
	if err := encoder.emit(RecordRadarStart, metadata, nil); err != nil {
		return encoder.poison(err)
	}
	source.radarStarted = true
	return nil
}

// WriteItem emits one provisional raw item. Item indices are independently
// zero-based and strictly increasing for each source.
func (encoder *Encoder) WriteItem(item Item) error {
	if err := encoder.requireActive(); err != nil {
		return err
	}
	source, exists := encoder.sources[item.SourceID]
	if !exists {
		return encoder.poison(fmt.Errorf("ITEM names undeclared source %q", item.SourceID))
	}
	if source.ended {
		return encoder.poison(fmt.Errorf("ITEM follows END for source %q", item.SourceID))
	}
	if source.contract.Kind == SourceRadar && !source.radarStarted {
		return encoder.poison(fmt.Errorf("radar source %q ITEM requires a preceding RADAR_START", item.SourceID))
	}
	if encoder.endingStarted {
		return encoder.poison(errors.New("ITEM cannot follow the first source END"))
	}
	if item.ItemIndex != source.nextItem {
		return encoder.poison(fmt.Errorf(
			"source %q item_index is %d; expected %d",
			item.SourceID,
			item.ItemIndex,
			source.nextItem,
		))
	}
	payloadBytes := uint64(len(item.Payload))
	if payloadBytes == 0 || payloadBytes > source.contract.Limits.MaxItemBytes {
		return encoder.poison(fmt.Errorf("source %q ITEM payload size %d is outside its limit", item.SourceID, payloadBytes))
	}
	if source.nextItem >= source.contract.Limits.MaxItems {
		return encoder.poison(fmt.Errorf("source %q exceeds max_items", item.SourceID))
	}
	if payloadBytes > source.contract.Limits.MaxPayloadBytes-source.payloadBytes {
		return encoder.poison(fmt.Errorf("source %q exceeds max_payload_bytes", item.SourceID))
	}
	if err := checkedItemTime(source.contract.Clock, item.Tick, item.WrapCount, item.DurationTicks); err != nil {
		return encoder.poison(fmt.Errorf("source %q ITEM time: %w", item.SourceID, err))
	}
	metadata, err := encodeMetadata(itemRecordV1{
		Schema: ItemSchemaV1, SourceID: item.SourceID, ItemIndex: item.ItemIndex,
		Provisional: true,
		Tick:        item.Tick, WrapCount: item.WrapCount, DurationTicks: item.DurationTicks,
		SyncEventID: item.SyncEventID,
	})
	if err != nil {
		return encoder.poison(err)
	}
	if err := encoder.emit(RecordItem, metadata, item.Payload); err != nil {
		return encoder.poison(err)
	}
	written, err := source.payloadHash.Write(item.Payload)
	if err != nil || written != len(item.Payload) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return encoder.poison(fmt.Errorf("hash source %q ITEM: %w", item.SourceID, err))
	}
	source.payloadBytes += payloadBytes
	source.nextItem++
	encoder.recordsStarted = true
	return nil
}

// EndSource binds the source”s provisional item count, byte count, and
// concatenated payload SHA-256.
func (encoder *Encoder) EndSource(sourceID string, outcome SourceOutcome) error {
	if err := encoder.requireActive(); err != nil {
		return err
	}
	source, exists := encoder.sources[sourceID]
	if !exists {
		return encoder.poison(fmt.Errorf("END names undeclared source %q", sourceID))
	}
	if source.ended {
		return encoder.poison(fmt.Errorf("source %q already emitted END", sourceID))
	}
	if err := validateSourceOutcome(outcome); err != nil {
		return encoder.poison(err)
	}
	metadata, err := encodeMetadata(endRecordV1{
		Schema: EndSchemaV1, SourceID: sourceID, Outcome: outcome, ItemCount: source.nextItem,
		PayloadBytes: source.payloadBytes, PayloadSHA256: currentDigestString(source.payloadHash),
	})
	if err != nil {
		return encoder.poison(err)
	}
	if err := encoder.emit(RecordEnd, metadata, nil); err != nil {
		return encoder.poison(err)
	}
	source.ended = true
	source.outcome = outcome
	encoder.recordsStarted = true
	encoder.endingStarted = true
	return nil
}

// Commit emits COMMIT followed immediately by the explicit EOF record. The
// artifact is the already-published aggregate session.json evidence supplied
// by the coordinator.
func (encoder *Encoder) Commit(artifact SessionArtifact) error {
	if err := encoder.requireActive(); err != nil {
		return err
	}
	if err := encoder.validateCommitOutcomes(); err != nil {
		return encoder.poison(err)
	}
	if err := validateArtifact(artifact); err != nil {
		return encoder.poison(err)
	}
	metadata, err := encodeMetadata(commitRecordV1{
		Schema: TerminalSchemaV1, SessionID: encoder.sessionID, Outcome: "commit",
		SessionJSONBytes: artifact.SizeBytes, SessionJSONSHA256: digestString(artifact.SHA256),
	})
	if err != nil {
		return encoder.poison(err)
	}
	return encoder.emitTerminal(RecordCommit, metadata)
}

// Abort emits one stable reason code followed immediately by the explicit EOF
// record. It does not promote any preceding provisional item.
func (encoder *Encoder) Abort(reason AbortReason) error {
	if err := encoder.requireActive(); err != nil {
		return err
	}
	if err := validateAbortReason(reason); err != nil {
		return encoder.poison(err)
	}
	metadata, err := encodeMetadata(abortRecordV1{
		Schema: TerminalSchemaV1, SessionID: encoder.sessionID, Outcome: "abort", ReasonCode: reason,
	})
	if err != nil {
		return encoder.poison(err)
	}
	return encoder.emitTerminal(RecordAbort, metadata)
}

func (encoder *Encoder) emitTerminal(recordType RecordType, metadata []byte) error {
	if err := encoder.emit(recordType, metadata, nil); err != nil {
		return encoder.poison(err)
	}
	eof, err := encodeMetadata(eofRecordV1{Schema: EOFSchemaV1, SessionID: encoder.sessionID})
	if err != nil {
		return encoder.poison(err)
	}
	if err := encoder.emit(RecordEOF, eof, nil); err != nil {
		return encoder.poison(err)
	}
	encoder.state = encoderTerminal
	return nil
}

func (encoder *Encoder) emit(recordType RecordType, metadata, payload []byte) error {
	if encoder.nextRecord == math.MaxUint64 {
		return errors.New("multisensor stream record_seq is exhausted")
	}
	if err := writeRecord(encoder.writer, Record{
		Type: recordType, RecordSeq: encoder.nextRecord, Metadata: metadata, Payload: payload,
	}); err != nil {
		return err
	}
	encoder.nextRecord++
	return nil
}

func (encoder *Encoder) validateCommitOutcomes() error {
	for _, source := range encoder.sources {
		if !source.ended {
			return errors.New("all declared sources must emit END before COMMIT")
		}
		if source.contract.Required && source.outcome != OutcomeComplete {
			return fmt.Errorf(
				"required source %q has outcome %q; COMMIT requires complete",
				source.contract.SourceID,
				source.outcome,
			)
		}
		if source.contract.Kind == SourceRadar && source.outcome == OutcomeComplete &&
			source.nextItem != 0 && !source.radarStarted {
			return fmt.Errorf("complete nonempty radar source %q requires RADAR_START", source.contract.SourceID)
		}
	}
	return nil
}

func (encoder *Encoder) requireActive() error {
	if encoder == nil || encoder.writer == nil {
		return errors.New("multisensor stream encoder is nil")
	}
	switch encoder.state {
	case encoderActive:
		return nil
	case encoderPoisoned:
		return fmt.Errorf("%w: %v", ErrEncoderPoisoned, encoder.failure)
	case encoderTerminal:
		return ErrEncoderTerminal
	default:
		return errors.New("multisensor stream encoder has invalid state")
	}
}

func (encoder *Encoder) poison(err error) error {
	if encoder.state != encoderPoisoned {
		encoder.state = encoderPoisoned
		encoder.failure = err
	}
	return err
}

func encodeMetadata(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode multisensor stream metadata: %w", err)
	}
	if len(encoded) > MaximumRecordMetadataBytes {
		return nil, fmt.Errorf("%w: record metadata is %d bytes", ErrLimit, len(encoded))
	}
	return encoded, nil
}

func currentDigestString(hash hash.Hash) string {
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digestString(digest)
}

package multisensorstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

type decoderPhase uint8

const (
	decoderInitial decoderPhase = iota
	decoderActive
	decoderTerminal
	decoderEOF
	decoderComplete
	decoderPoisoned
)

// Decoder validates framing, record_seq, record state, per-source item order,
// and END lineage while yielding the original metadata and raw payload.
type Decoder struct {
	reader         io.Reader
	nextRecord     uint64
	phase          decoderPhase
	sessionID      string
	sources        map[string]*sourceProgress
	configWritten  bool
	recordsStarted bool
	endingStarted  bool
	failure        error
}

func NewDecoder(reader io.Reader) (*Decoder, error) {
	if reader == nil {
		return nil, errors.New("multisensor stream reader is nil")
	}
	return &Decoder{reader: reader, phase: decoderInitial}, nil
}

// Read returns the explicit EOF record normally. The following call must see
// transport EOF; missing or trailing bytes are protocol failures.
func (decoder *Decoder) Read() (Record, error) {
	var zero Record
	if decoder == nil || decoder.reader == nil {
		return zero, errors.New("multisensor stream decoder is nil")
	}
	if decoder.phase == decoderPoisoned {
		return zero, fmt.Errorf("%w: %v", ErrDecoderPoisoned, decoder.failure)
	}
	if decoder.phase == decoderComplete {
		return zero, io.EOF
	}
	record, err := readRecord(decoder.reader)
	if errors.Is(err, io.EOF) {
		if decoder.phase != decoderEOF {
			return zero, decoder.poison(fmt.Errorf("%w: transport EOF arrived before the explicit EOF record", ErrProtocol))
		}
		decoder.phase = decoderComplete
		return zero, io.EOF
	}
	if err != nil {
		return zero, decoder.poison(err)
	}
	if record.RecordSeq != decoder.nextRecord {
		return zero, decoder.poison(fmt.Errorf(
			"%w: record_seq is %d; expected %d",
			ErrProtocol,
			record.RecordSeq,
			decoder.nextRecord,
		))
	}
	if record.RecordSeq == math.MaxUint64 {
		return zero, decoder.poison(fmt.Errorf("%w: record_seq is exhausted", ErrProtocol))
	}
	if err := decoder.accept(record); err != nil {
		return zero, decoder.poison(err)
	}
	decoder.nextRecord++
	return record, nil
}

func (decoder *Decoder) accept(record Record) error {
	switch record.Type {
	case RecordSession:
		return decoder.acceptSession(record)
	case RecordRadarConfig:
		return decoder.acceptRadarConfig(record)
	case RecordItem:
		return decoder.acceptItem(record)
	case RecordEnd:
		return decoder.acceptEnd(record)
	case RecordCommit:
		return decoder.acceptCommit(record)
	case RecordAbort:
		return decoder.acceptAbort(record)
	case RecordEOF:
		return decoder.acceptEOF(record)
	default:
		return fmt.Errorf("%w: unknown record type %d", ErrProtocol, record.Type)
	}
}

func (decoder *Decoder) acceptSession(record Record) error {
	if decoder.phase != decoderInitial || record.RecordSeq != 0 {
		return fmt.Errorf("%w: SESSION is invalid after stream start", ErrProtocol)
	}
	var wire sessionRecordV1
	if err := decodeExactMetadata(
		record.Metadata,
		&wire,
		"schema", "session_id", "synchronization_grade", "sources",
	); err != nil {
		return fmt.Errorf("%w: invalid SESSION metadata: %v", ErrProtocol, err)
	}
	if wire.Schema != SchemaV1 {
		return fmt.Errorf("%w: SESSION schema %q is invalid", ErrProtocol, wire.Schema)
	}
	session := Session{
		SessionID: wire.SessionID, SynchronizationGrade: wire.SynchronizationGrade, Sources: wire.Sources,
	}
	if err := validateSession(session); err != nil {
		return fmt.Errorf("%w: invalid SESSION contract: %v", ErrProtocol, err)
	}
	decoder.sessionID = session.SessionID
	decoder.sources = make(map[string]*sourceProgress, len(session.Sources))
	for _, source := range session.Sources {
		decoder.sources[source.SourceID] = &sourceProgress{contract: source, payloadHash: sha256.New()}
	}
	decoder.phase = decoderActive
	return nil
}

func (decoder *Decoder) acceptRadarConfig(record Record) error {
	if decoder.phase != decoderActive || decoder.configWritten || decoder.recordsStarted {
		return fmt.Errorf("%w: RADAR_CONFIG must be unique and precede ITEM/END records", ErrProtocol)
	}
	var wire radarConfigRecordV1
	if err := decodeExactMetadata(
		record.Metadata,
		&wire,
		"schema", "source_id", "format", "size_bytes", "sha256",
	); err != nil {
		return fmt.Errorf("%w: invalid RADAR_CONFIG metadata: %v", ErrProtocol, err)
	}
	source, exists := decoder.sources[wire.SourceID]
	if wire.Schema != RadarConfigSchemaV1 || !exists || source.contract.Kind != SourceRadar {
		return fmt.Errorf("%w: RADAR_CONFIG contract is invalid", ErrProtocol)
	}
	if !formatPattern.MatchString(wire.Format) || wire.SizeBytes != uint64(len(record.Payload)) || !validDigest(wire.SHA256) {
		return fmt.Errorf("%w: RADAR_CONFIG format, size, or digest is invalid", ErrProtocol)
	}
	digest := sha256.Sum256(record.Payload)
	if wire.SHA256 != digestString(digest) {
		return fmt.Errorf("%w: RADAR_CONFIG SHA-256 does not match its payload", ErrProtocol)
	}
	decoder.configWritten = true
	return nil
}

func (decoder *Decoder) acceptItem(record Record) error {
	if decoder.phase != decoderActive {
		return fmt.Errorf("%w: ITEM is invalid outside the active phase", ErrProtocol)
	}
	if decoder.endingStarted {
		return fmt.Errorf("%w: ITEM cannot follow the first source END", ErrProtocol)
	}
	var wire itemRecordV1
	if err := decodeExactMetadata(
		record.Metadata,
		&wire,
		"schema", "source_id", "item_index", "provisional", "tick", "wrap_count", "duration_ticks", "sync_event_id",
	); err != nil {
		return fmt.Errorf("%w: invalid ITEM metadata: %v", ErrProtocol, err)
	}
	source, exists := decoder.sources[wire.SourceID]
	if wire.Schema != ItemSchemaV1 || !wire.Provisional || !exists || source.ended {
		return fmt.Errorf("%w: ITEM source or state is invalid", ErrProtocol)
	}
	if wire.ItemIndex != source.nextItem {
		return fmt.Errorf(
			"%w: source %q item_index is %d; expected %d",
			ErrProtocol,
			wire.SourceID,
			wire.ItemIndex,
			source.nextItem,
		)
	}
	payloadBytes := uint64(len(record.Payload))
	if source.nextItem >= source.contract.Limits.MaxItems ||
		payloadBytes > source.contract.Limits.MaxItemBytes ||
		payloadBytes > source.contract.Limits.MaxPayloadBytes-source.payloadBytes {
		return fmt.Errorf("%w: source %q ITEM exceeds its static limits", ErrLimit, wire.SourceID)
	}
	if err := checkedItemTime(source.contract.Clock, wire.Tick, wire.WrapCount, wire.DurationTicks); err != nil {
		return fmt.Errorf("%w: source %q ITEM time: %v", ErrProtocol, wire.SourceID, err)
	}
	written, err := source.payloadHash.Write(record.Payload)
	if err != nil || written != len(record.Payload) {
		return fmt.Errorf("%w: hash source %q ITEM", ErrProtocol, wire.SourceID)
	}
	source.payloadBytes += payloadBytes
	source.nextItem++
	decoder.recordsStarted = true
	return nil
}

func (decoder *Decoder) acceptEnd(record Record) error {
	if decoder.phase != decoderActive {
		return fmt.Errorf("%w: END is invalid outside the active phase", ErrProtocol)
	}
	var wire endRecordV1
	if err := decodeExactMetadata(
		record.Metadata,
		&wire,
		"schema", "source_id", "item_count", "payload_bytes", "payload_sha256",
	); err != nil {
		return fmt.Errorf("%w: invalid END metadata: %v", ErrProtocol, err)
	}
	source, exists := decoder.sources[wire.SourceID]
	if wire.Schema != EndSchemaV1 || !exists || source.ended || !validDigest(wire.PayloadSHA256) {
		return fmt.Errorf("%w: END source, state, or digest is invalid", ErrProtocol)
	}
	if wire.ItemCount != source.nextItem || wire.PayloadBytes != source.payloadBytes ||
		wire.PayloadSHA256 != currentDigestString(source.payloadHash) {
		return fmt.Errorf("%w: END does not match provisional source payload", ErrProtocol)
	}
	source.ended = true
	decoder.recordsStarted = true
	decoder.endingStarted = true
	return nil
}

func (decoder *Decoder) acceptCommit(record Record) error {
	if decoder.phase != decoderActive || !decoder.allSourcesEnded() {
		return fmt.Errorf("%w: COMMIT requires END from every source", ErrProtocol)
	}
	var wire commitRecordV1
	if err := decodeExactMetadata(
		record.Metadata,
		&wire,
		"schema", "session_id", "outcome", "session_json_size_bytes", "session_json_sha256",
	); err != nil {
		return fmt.Errorf("%w: invalid COMMIT metadata: %v", ErrProtocol, err)
	}
	if wire.Schema != TerminalSchemaV1 || wire.SessionID != decoder.sessionID || wire.Outcome != "commit" ||
		!validDigest(wire.SessionJSONSHA256) {
		return fmt.Errorf("%w: COMMIT contract is invalid", ErrProtocol)
	}
	decoded, _ := hex.DecodeString(wire.SessionJSONSHA256)
	var digest [sha256.Size]byte
	copy(digest[:], decoded)
	if err := validateArtifact(SessionArtifact{SizeBytes: wire.SessionJSONBytes, SHA256: digest}); err != nil {
		return fmt.Errorf("%w: invalid COMMIT artifact: %v", ErrProtocol, err)
	}
	decoder.phase = decoderTerminal
	return nil
}

func (decoder *Decoder) acceptAbort(record Record) error {
	if decoder.phase != decoderActive {
		return fmt.Errorf("%w: ABORT is invalid outside the active phase", ErrProtocol)
	}
	var wire abortRecordV1
	if err := decodeExactMetadata(
		record.Metadata,
		&wire,
		"schema", "session_id", "outcome", "reason_code",
	); err != nil {
		return fmt.Errorf("%w: invalid ABORT metadata: %v", ErrProtocol, err)
	}
	if wire.Schema != TerminalSchemaV1 || wire.SessionID != decoder.sessionID || wire.Outcome != "abort" {
		return fmt.Errorf("%w: ABORT contract is invalid", ErrProtocol)
	}
	if err := validateAbortReason(wire.ReasonCode); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	decoder.phase = decoderTerminal
	return nil
}

func (decoder *Decoder) acceptEOF(record Record) error {
	if decoder.phase != decoderTerminal {
		return fmt.Errorf("%w: explicit EOF must immediately follow COMMIT or ABORT", ErrProtocol)
	}
	var wire eofRecordV1
	if err := decodeExactMetadata(record.Metadata, &wire, "schema", "session_id"); err != nil {
		return fmt.Errorf("%w: invalid EOF metadata: %v", ErrProtocol, err)
	}
	if wire.Schema != EOFSchemaV1 || wire.SessionID != decoder.sessionID {
		return fmt.Errorf("%w: EOF contract is invalid", ErrProtocol)
	}
	decoder.phase = decoderEOF
	return nil
}

func (decoder *Decoder) allSourcesEnded() bool {
	for _, source := range decoder.sources {
		if !source.ended {
			return false
		}
	}
	return true
}

func (decoder *Decoder) poison(err error) error {
	if decoder.phase != decoderPoisoned {
		decoder.phase = decoderPoisoned
		decoder.failure = err
	}
	return err
}

func decodeExactMetadata(data []byte, target any, keys ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != len(keys) {
		return errors.New("metadata has an invalid exact key set")
	}
	for _, key := range keys {
		if _, exists := fields[key]; !exists {
			return errors.New("metadata has an invalid exact key set")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("metadata contains multiple JSON values")
		}
		return err
	}
	return nil
}

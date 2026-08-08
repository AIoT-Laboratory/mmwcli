package sensorproducer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	ProtocolVersion = uint16(1)
	FrameMagic      = "MMWSPRD1"

	MaxIDBytes       = 128
	MaxControlBytes  = 4096
	MaxMetadataBytes = 64 << 10
	MaxPayloadBytes  = 64 << 20
	MaxErrorBytes    = 4096

	framePrefixSize = 40
	frameHeaderSize = framePrefixSize + sha256.Size
)

var (
	ErrProtocol = errors.New("sensor producer protocol error")
	ErrLimit    = errors.New("sensor producer limit exceeded")
)

type Command string

const (
	CommandReady  Command = "READY"
	CommandArm    Command = "ARM"
	CommandStart  Command = "START"
	CommandStop   Command = "STOP"
	CommandCancel Command = "CANCEL"
)

func (command Command) valid() bool {
	switch command {
	case CommandReady, CommandArm, CommandStart, CommandStop, CommandCancel:
		return true
	default:
		return false
	}
}

type Control struct {
	Version   uint16  `json:"version"`
	Command   Command `json:"command"`
	SessionID string  `json:"session_id"`
	SourceID  string  `json:"source_id"`
	Seq       uint64  `json:"seq"`
}

func (control Control) validate() error {
	if control.Version != ProtocolVersion {
		return fmt.Errorf("%w: control version %d; expected %d", ErrProtocol, control.Version, ProtocolVersion)
	}
	if !control.Command.valid() {
		return fmt.Errorf("%w: unknown control command %q", ErrProtocol, control.Command)
	}
	if err := validateID("session_id", control.SessionID); err != nil {
		return err
	}
	if err := validateID("source_id", control.SourceID); err != nil {
		return err
	}
	if control.Seq == 0 {
		return fmt.Errorf("%w: control sequence must be positive", ErrProtocol)
	}
	return nil
}

type ControlEncoder struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewControlEncoder(writer io.Writer) (*ControlEncoder, error) {
	if writer == nil {
		return nil, errors.New("sensor producer control writer is nil")
	}
	return &ControlEncoder{writer: writer}, nil
}

func (encoder *ControlEncoder) Write(control Control) error {
	if encoder == nil || encoder.writer == nil {
		return errors.New("sensor producer control encoder is nil")
	}
	if err := control.validate(); err != nil {
		return err
	}
	line, err := json.Marshal(control)
	if err != nil {
		return fmt.Errorf("encode sensor producer control: %w", err)
	}
	if len(line)+1 > MaxControlBytes {
		return fmt.Errorf("%w: control line is %d bytes; maximum is %d", ErrLimit, len(line)+1, MaxControlBytes)
	}
	line = append(line, '\n')
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := writeAll(encoder.writer, line); err != nil {
		return fmt.Errorf("write sensor producer control: %w", err)
	}
	return nil
}

type ControlDecoder struct {
	mu     sync.Mutex
	reader *bufio.Reader
}

func NewControlDecoder(reader io.Reader) (*ControlDecoder, error) {
	if reader == nil {
		return nil, errors.New("sensor producer control reader is nil")
	}
	return &ControlDecoder{reader: bufio.NewReaderSize(reader, MaxControlBytes+1)}, nil
}

func (decoder *ControlDecoder) Read() (Control, error) {
	var control Control
	if decoder == nil || decoder.reader == nil {
		return control, errors.New("sensor producer control decoder is nil")
	}
	decoder.mu.Lock()
	defer decoder.mu.Unlock()
	line, err := decoder.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return control, fmt.Errorf("%w: control line exceeds %d bytes", ErrLimit, MaxControlBytes)
	}
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return control, io.EOF
		}
		return control, fmt.Errorf("%w: truncated control line without newline", ErrProtocol)
	}
	if err != nil {
		return control, fmt.Errorf("read sensor producer control: %w", err)
	}
	if len(line) > MaxControlBytes {
		return control, fmt.Errorf("%w: control line is %d bytes; maximum is %d", ErrLimit, len(line), MaxControlBytes)
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if err := decodeStrictJSON(line, &control); err != nil {
		return control, fmt.Errorf("%w: invalid control JSON: %v", ErrProtocol, err)
	}
	if err := control.validate(); err != nil {
		return control, err
	}
	return control, nil
}

type FrameType uint16

const (
	FrameACK FrameType = iota + 1
	FrameSession
	FrameItem
	FrameEnd
	FrameError
	FrameEOF
)

func (frameType FrameType) valid() bool {
	return frameType >= FrameACK && frameType <= FrameEOF
}

type Record struct {
	Type      FrameType
	SessionID string
	SourceID  string
	Seq       uint64
	Metadata  json.RawMessage
	Payload   []byte
}

func (record Record) validate() error {
	if !record.Type.valid() {
		return fmt.Errorf("%w: unknown frame type %d", ErrProtocol, record.Type)
	}
	if err := validateID("session_id", record.SessionID); err != nil {
		return err
	}
	if err := validateID("source_id", record.SourceID); err != nil {
		return err
	}
	if record.Seq == 0 {
		return fmt.Errorf("%w: frame sequence must be positive", ErrProtocol)
	}
	if len(record.Metadata) > MaxMetadataBytes {
		return fmt.Errorf("%w: metadata is %d bytes; maximum is %d", ErrLimit, len(record.Metadata), MaxMetadataBytes)
	}
	if !isJSONObject(record.Metadata) {
		return fmt.Errorf("%w: frame metadata must be one JSON object", ErrProtocol)
	}
	if len(record.Payload) > MaxPayloadBytes {
		return fmt.Errorf("%w: payload is %d bytes; maximum is %d", ErrLimit, len(record.Payload), MaxPayloadBytes)
	}
	if record.Type != FrameItem && len(record.Payload) != 0 {
		return fmt.Errorf("%w: frame type %d cannot carry a raw payload", ErrProtocol, record.Type)
	}
	return nil
}

type AckMetadata struct {
	Command Command `json:"command"`
	OK      bool    `json:"ok"`
	Error   string  `json:"error,omitempty"`
}

func NewACKRecord(control Control, ok bool, errorText string) (Record, error) {
	if err := control.validate(); err != nil {
		return Record{}, err
	}
	if ok && errorText != "" {
		return Record{}, fmt.Errorf("%w: successful ACK cannot carry an error", ErrProtocol)
	}
	if !ok && errorText == "" {
		return Record{}, fmt.Errorf("%w: rejected ACK requires an error", ErrProtocol)
	}
	if len(errorText) > MaxErrorBytes {
		return Record{}, fmt.Errorf("%w: ACK error exceeds %d bytes", ErrLimit, MaxErrorBytes)
	}
	metadata, err := json.Marshal(AckMetadata{Command: control.Command, OK: ok, Error: errorText})
	if err != nil {
		return Record{}, err
	}
	return Record{
		Type: FrameACK, SessionID: control.SessionID, SourceID: control.SourceID,
		Seq: control.Seq, Metadata: metadata,
	}, nil
}

func DecodeACK(record Record) (AckMetadata, error) {
	var metadata AckMetadata
	if record.Type != FrameACK || len(record.Payload) != 0 {
		return metadata, fmt.Errorf("%w: record is not an ACK", ErrProtocol)
	}
	if err := decodeStrictJSON(record.Metadata, &metadata); err != nil {
		return metadata, fmt.Errorf("%w: invalid ACK metadata: %v", ErrProtocol, err)
	}
	if !metadata.Command.valid() {
		return metadata, fmt.Errorf("%w: ACK command %q is invalid", ErrProtocol, metadata.Command)
	}
	if metadata.OK && metadata.Error != "" {
		return metadata, fmt.Errorf("%w: successful ACK carries an error", ErrProtocol)
	}
	if !metadata.OK && metadata.Error == "" {
		return metadata, fmt.Errorf("%w: rejected ACK has no error", ErrProtocol)
	}
	if len(metadata.Error) > MaxErrorBytes {
		return metadata, fmt.Errorf("%w: ACK error exceeds %d bytes", ErrLimit, MaxErrorBytes)
	}
	return metadata, nil
}

type Encoder struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewEncoder(writer io.Writer) (*Encoder, error) {
	if writer == nil {
		return nil, errors.New("sensor producer frame writer is nil")
	}
	return &Encoder{writer: writer}, nil
}

func (encoder *Encoder) Write(record Record) error {
	if encoder == nil || encoder.writer == nil {
		return errors.New("sensor producer frame encoder is nil")
	}
	if err := record.validate(); err != nil {
		return err
	}
	header := make([]byte, frameHeaderSize)
	copy(header[:8], FrameMagic)
	binary.LittleEndian.PutUint16(header[8:10], ProtocolVersion)
	binary.LittleEndian.PutUint16(header[10:12], uint16(record.Type))
	binary.LittleEndian.PutUint64(header[16:24], record.Seq)
	binary.LittleEndian.PutUint16(header[24:26], uint16(len(record.SessionID)))
	binary.LittleEndian.PutUint16(header[26:28], uint16(len(record.SourceID)))
	binary.LittleEndian.PutUint32(header[28:32], uint32(len(record.Metadata)))
	binary.LittleEndian.PutUint64(header[32:40], uint64(len(record.Payload)))
	digest := sha256.New()
	_, _ = digest.Write(header[:framePrefixSize])
	_, _ = io.WriteString(digest, record.SessionID)
	_, _ = io.WriteString(digest, record.SourceID)
	_, _ = digest.Write(record.Metadata)
	_, _ = digest.Write(record.Payload)
	copy(header[framePrefixSize:], digest.Sum(nil))

	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	for _, part := range [][]byte{header, []byte(record.SessionID), []byte(record.SourceID), record.Metadata, record.Payload} {
		if err := writeAll(encoder.writer, part); err != nil {
			return fmt.Errorf("write sensor producer frame: %w", err)
		}
	}
	return nil
}

type Decoder struct {
	mu     sync.Mutex
	reader io.Reader
}

func NewDecoder(reader io.Reader) (*Decoder, error) {
	if reader == nil {
		return nil, errors.New("sensor producer frame reader is nil")
	}
	return &Decoder{reader: reader}, nil
}

func (decoder *Decoder) Read() (Record, error) {
	var record Record
	if decoder == nil || decoder.reader == nil {
		return record, errors.New("sensor producer frame decoder is nil")
	}
	decoder.mu.Lock()
	defer decoder.mu.Unlock()
	header := make([]byte, frameHeaderSize)
	read, err := io.ReadFull(decoder.reader, header)
	if errors.Is(err, io.EOF) && read == 0 {
		return record, io.EOF
	}
	if err != nil {
		return record, fmt.Errorf("%w: read frame header: %v", ErrProtocol, err)
	}
	if string(header[:8]) != FrameMagic {
		return record, fmt.Errorf("%w: invalid frame magic", ErrProtocol)
	}
	if version := binary.LittleEndian.Uint16(header[8:10]); version != ProtocolVersion {
		return record, fmt.Errorf("%w: frame version %d; expected %d", ErrProtocol, version, ProtocolVersion)
	}
	if binary.LittleEndian.Uint32(header[12:16]) != 0 {
		return record, fmt.Errorf("%w: frame flags must be zero", ErrProtocol)
	}
	record.Type = FrameType(binary.LittleEndian.Uint16(header[10:12]))
	record.Seq = binary.LittleEndian.Uint64(header[16:24])
	sessionLength := uint64(binary.LittleEndian.Uint16(header[24:26]))
	sourceLength := uint64(binary.LittleEndian.Uint16(header[26:28]))
	metadataLength := uint64(binary.LittleEndian.Uint32(header[28:32]))
	payloadLength := binary.LittleEndian.Uint64(header[32:40])
	if sessionLength == 0 || sessionLength > MaxIDBytes || sourceLength == 0 || sourceLength > MaxIDBytes {
		return record, fmt.Errorf("%w: frame identifier length is invalid", ErrLimit)
	}
	if metadataLength > MaxMetadataBytes || payloadLength > MaxPayloadBytes {
		return record, fmt.Errorf("%w: frame metadata or payload length exceeds its limit", ErrLimit)
	}
	total := sessionLength + sourceLength + metadataLength + payloadLength
	if total > uint64(2*MaxIDBytes+MaxMetadataBytes+MaxPayloadBytes) {
		return record, fmt.Errorf("%w: frame body length overflows its limit", ErrLimit)
	}
	body := make([]byte, int(total))
	if _, err := io.ReadFull(decoder.reader, body); err != nil {
		return record, fmt.Errorf("%w: read frame body: %v", ErrProtocol, err)
	}
	digest := sha256.New()
	_, _ = digest.Write(header[:framePrefixSize])
	_, _ = digest.Write(body)
	if !bytes.Equal(header[framePrefixSize:], digest.Sum(nil)) {
		return record, fmt.Errorf("%w: frame digest mismatch", ErrProtocol)
	}
	sessionEnd := int(sessionLength)
	sourceEnd := sessionEnd + int(sourceLength)
	metadataEnd := sourceEnd + int(metadataLength)
	record.SessionID = string(body[:sessionEnd])
	record.SourceID = string(body[sessionEnd:sourceEnd])
	record.Metadata = body[sourceEnd:metadataEnd]
	record.Payload = body[metadataEnd:]
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateID(name, value string) error {
	if len(value) == 0 || len(value) > MaxIDBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s must contain 1..%d valid UTF-8 bytes", ErrProtocol, name, MaxIDBytes)
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return fmt.Errorf("%w: %s contains unsupported character %q", ErrProtocol, name, character)
	}
	return nil
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

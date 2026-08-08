// Package multisensorstream defines the local, transport-independent
// mmwcli multi-sensor aggregate stream v1 wire format.
package multisensorstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ProtocolMajor    = uint16(1)
	RecordHeaderSize = 80

	MaximumRecordMetadataBytes = 1 << 20
	MaximumRadarConfigBytes    = 4 << 20
)

const recordDigestDomain = "mmwcli.multisensor_stream.record.v1\x00"

var (
	recordMagic = [8]byte{'M', 'M', 'W', 'M', 'S', 'T', 'R', '1'}

	ErrProtocol        = errors.New("multisensor stream protocol error")
	ErrLimit           = errors.New("multisensor stream limit exceeded")
	ErrEncoderPoisoned = errors.New("multisensor stream encoder is poisoned")
	ErrEncoderTerminal = errors.New("multisensor stream encoder is terminal")
	ErrDecoderPoisoned = errors.New("multisensor stream decoder is poisoned")
)

type RecordType uint16

const (
	RecordSession RecordType = iota + 1
	RecordRadarConfig
	RecordItem
	RecordEnd
	RecordCommit
	RecordAbort
	RecordEOF
)

func (recordType RecordType) valid() bool {
	return recordType >= RecordSession && recordType <= RecordEOF
}

type Record struct {
	Type      RecordType
	RecordSeq uint64
	Metadata  json.RawMessage
	Payload   []byte
}

// recordDigest detects transport corruption. It is not authentication or
// source identity evidence.
func recordDigest(headerPrefix, metadata, payload []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = io.WriteString(hash, recordDigestDomain)
	_, _ = hash.Write(headerPrefix)
	_, _ = hash.Write(metadata)
	_, _ = hash.Write(payload)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func writeRecord(writer io.Writer, record Record) error {
	if writer == nil {
		return errors.New("multisensor stream writer is nil")
	}
	if !record.Type.valid() {
		return fmt.Errorf("%w: unknown record type %d", ErrProtocol, record.Type)
	}
	if len(record.Metadata) == 0 || len(record.Metadata) > MaximumRecordMetadataBytes ||
		!isJSONObject(record.Metadata) {
		return fmt.Errorf("%w: record metadata must be one bounded JSON object", ErrProtocol)
	}
	if err := validateWirePayload(record.Type, uint64(len(record.Payload))); err != nil {
		return err
	}

	header := make([]byte, RecordHeaderSize)
	copy(header[0:8], recordMagic[:])
	binary.LittleEndian.PutUint16(header[8:10], ProtocolMajor)
	binary.LittleEndian.PutUint16(header[10:12], RecordHeaderSize)
	binary.LittleEndian.PutUint16(header[12:14], uint16(record.Type))
	// Bytes 14..15 are flags and 40..47 are reserved. Both are zero in v1.
	binary.LittleEndian.PutUint64(header[16:24], record.RecordSeq)
	binary.LittleEndian.PutUint64(header[24:32], uint64(len(record.Metadata)))
	binary.LittleEndian.PutUint64(header[32:40], uint64(len(record.Payload)))
	digest := recordDigest(header[:48], record.Metadata, record.Payload)
	copy(header[48:80], digest[:])

	for _, part := range [][]byte{header, record.Metadata, record.Payload} {
		if len(part) == 0 {
			continue
		}
		if err := writeExact(writer, part); err != nil {
			return fmt.Errorf("write multisensor stream record: %w", err)
		}
	}
	return nil
}

func readRecord(reader io.Reader) (Record, error) {
	var record Record
	if reader == nil {
		return record, errors.New("multisensor stream reader is nil")
	}
	header := make([]byte, RecordHeaderSize)
	read, err := io.ReadFull(reader, header)
	if errors.Is(err, io.EOF) && read == 0 {
		return record, io.EOF
	}
	if err != nil {
		return record, fmt.Errorf("%w: read record header: %v", ErrProtocol, err)
	}
	if !bytes.Equal(header[:8], recordMagic[:]) {
		return record, fmt.Errorf("%w: invalid record magic", ErrProtocol)
	}
	if major := binary.LittleEndian.Uint16(header[8:10]); major != ProtocolMajor {
		return record, fmt.Errorf("%w: record major %d; expected %d", ErrProtocol, major, ProtocolMajor)
	}
	if size := binary.LittleEndian.Uint16(header[10:12]); size != RecordHeaderSize {
		return record, fmt.Errorf("%w: record header size %d; expected %d", ErrProtocol, size, RecordHeaderSize)
	}
	if binary.LittleEndian.Uint16(header[14:16]) != 0 || binary.LittleEndian.Uint64(header[40:48]) != 0 {
		return record, fmt.Errorf("%w: record flags and reserved bytes must be zero", ErrProtocol)
	}
	record.Type = RecordType(binary.LittleEndian.Uint16(header[12:14]))
	if !record.Type.valid() {
		return Record{}, fmt.Errorf("%w: unknown record type %d", ErrProtocol, record.Type)
	}
	record.RecordSeq = binary.LittleEndian.Uint64(header[16:24])
	metadataBytes := binary.LittleEndian.Uint64(header[24:32])
	payloadBytes := binary.LittleEndian.Uint64(header[32:40])
	if metadataBytes == 0 || metadataBytes > MaximumRecordMetadataBytes {
		return Record{}, fmt.Errorf("%w: record metadata length %d is invalid", ErrLimit, metadataBytes)
	}
	if err := validateWirePayload(record.Type, payloadBytes); err != nil {
		return Record{}, err
	}
	if metadataBytes > ^uint64(0)-payloadBytes {
		return Record{}, fmt.Errorf("%w: record body length overflows uint64", ErrLimit)
	}
	bodyBytes := metadataBytes + payloadBytes
	if bodyBytes > uint64(maxInt()) {
		return Record{}, fmt.Errorf("%w: record body cannot fit in memory", ErrLimit)
	}
	body := make([]byte, int(bodyBytes))
	if _, err := io.ReadFull(reader, body); err != nil {
		return Record{}, fmt.Errorf("%w: read record body: %v", ErrProtocol, err)
	}
	metadata := body[:int(metadataBytes)]
	payload := body[int(metadataBytes):]
	digest := recordDigest(header[:48], metadata, payload)
	if !bytes.Equal(header[48:80], digest[:]) {
		return Record{}, fmt.Errorf("%w: record digest mismatch", ErrProtocol)
	}
	if !isJSONObject(metadata) {
		return Record{}, fmt.Errorf("%w: record metadata is not one JSON object", ErrProtocol)
	}
	record.Metadata = metadata
	record.Payload = payload
	return record, nil
}

func validateWirePayload(recordType RecordType, size uint64) error {
	switch recordType {
	case RecordRadarConfig:
		if size == 0 || size > MaximumRadarConfigBytes {
			return fmt.Errorf("%w: RADAR_CONFIG payload size %d is invalid", ErrLimit, size)
		}
	case RecordItem:
		if size == 0 || size > multisensorMaximumItemBytes {
			return fmt.Errorf("%w: ITEM payload size %d is invalid", ErrLimit, size)
		}
	case RecordSession, RecordEnd, RecordCommit, RecordAbort, RecordEOF:
		if size != 0 {
			return fmt.Errorf("%w: record type %d cannot carry a raw payload", ErrProtocol, recordType)
		}
	default:
		return fmt.Errorf("%w: unknown record type %d", ErrProtocol, recordType)
	}
	return nil
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

func writeExact(writer io.Writer, data []byte) error {
	written, err := writer.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

const multisensorMaximumItemBytes = uint64(1 << 34)

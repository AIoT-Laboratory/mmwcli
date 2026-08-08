package capturestream

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

type decodedTestRecord struct {
	kind      recordType
	sequence  uint64
	itemIndex uint64
	payload   []byte
}

func TestWriteRecordUsesFixedLittleEndianIntegrityHeader(t *testing.T) {
	payload := []byte{1, 2, 3, 4}
	var stream bytes.Buffer
	if err := writeRecord(&stream, recordFrame, 9, 7, payload, len(payload)); err != nil {
		t.Fatal(err)
	}
	wire := stream.Bytes()
	if len(wire) != RecordHeaderSize+len(payload) {
		t.Fatalf("record size = %d", len(wire))
	}
	if !bytes.Equal(wire[0:8], recordMagic[:]) {
		t.Fatalf("magic = %q", wire[0:8])
	}
	if got := binary.LittleEndian.Uint16(wire[8:10]); got != protocolMajor {
		t.Fatalf("protocol major = %d", got)
	}
	if got := binary.LittleEndian.Uint16(wire[10:12]); got != RecordHeaderSize {
		t.Fatalf("header size = %d", got)
	}
	if got := recordType(binary.LittleEndian.Uint16(wire[12:14])); got != recordFrame {
		t.Fatalf("record type = %d", got)
	}
	if binary.LittleEndian.Uint16(wire[14:16]) != 0 ||
		binary.LittleEndian.Uint64(wire[40:48]) != 0 {
		t.Fatal("v1 flags or reserved field is non-zero")
	}
	if got := binary.LittleEndian.Uint64(wire[16:24]); got != 9 {
		t.Fatalf("sequence = %d", got)
	}
	if got := binary.LittleEndian.Uint64(wire[24:32]); got != 7 {
		t.Fatalf("item index = %d", got)
	}
	if got := binary.LittleEndian.Uint64(wire[32:40]); got != uint64(len(payload)) {
		t.Fatalf("payload bytes = %d", got)
	}
	digest := independentRecordDigest(wire[:48], payload)
	if !bytes.Equal(wire[48:80], digest[:]) {
		t.Fatalf("record digest = %x, want %x", wire[48:80], digest)
	}
	if !bytes.Equal(wire[80:], payload) {
		t.Fatalf("payload = %x", wire[80:])
	}
}

func TestRecordIntegrityCoversHeaderAndPayload(t *testing.T) {
	var stream bytes.Buffer
	if err := writeRecord(&stream, recordFrame, 2, 0, []byte{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{16, RecordHeaderSize} {
		corrupt := bytes.Clone(stream.Bytes())
		corrupt[offset] ^= 0x80
		if _, err := decodeTestRecords(corrupt); err == nil {
			t.Fatalf("decoder accepted corruption at offset %d", offset)
		}
	}
	truncated := stream.Bytes()[:stream.Len()-1]
	if _, err := decodeTestRecords(truncated); err == nil {
		t.Fatal("decoder accepted a truncated record")
	}
}

func TestWriteAllHandlesPartialProgressAndRejectsNoProgress(t *testing.T) {
	partial := &partialWriter{maximum: 3}
	if err := writeAll(partial, []byte("abcdefgh")); err != nil {
		t.Fatal(err)
	}
	if got := partial.String(); got != "abcdefgh" {
		t.Fatalf("partial writer output = %q", got)
	}
	if err := writeAll(zeroWriter{}, []byte{1}); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero-progress error = %v", err)
	}
	if err := writeAll(invalidCountWriter{}, []byte{1}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("invalid-count error = %v", err)
	}
}

func TestWriteRecordRejectsInvalidTypeAndBound(t *testing.T) {
	var stream bytes.Buffer
	if err := writeRecord(&stream, 0, 0, 0, nil, 0); err == nil {
		t.Fatal("invalid record type was accepted")
	}
	if err := writeRecord(&stream, recordFrame, 0, 0, []byte{1, 2}, 1); err == nil {
		t.Fatal("oversize record payload was accepted")
	}
	if stream.Len() != 0 {
		t.Fatal("invalid record wrote bytes")
	}
}

type partialWriter struct {
	bytes.Buffer
	maximum int
}

func (writer *partialWriter) Write(data []byte) (int, error) {
	if len(data) > writer.maximum {
		data = data[:writer.maximum]
	}
	return writer.Buffer.Write(data)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type invalidCountWriter struct{}

func (invalidCountWriter) Write(data []byte) (int, error) { return len(data) + 1, nil }

func independentRecordDigest(headerPrefix, payload []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(recordDigestDomain))
	_, _ = hash.Write(headerPrefix)
	_, _ = hash.Write(payload)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func decodeTestRecords(wire []byte) ([]decodedTestRecord, error) {
	var records []decodedTestRecord
	var expectedSequence uint64
	for len(wire) > 0 {
		if len(wire) < RecordHeaderSize {
			return nil, io.ErrUnexpectedEOF
		}
		header := wire[:RecordHeaderSize]
		if !bytes.Equal(header[0:8], recordMagic[:]) {
			return nil, errors.New("invalid record magic")
		}
		if binary.LittleEndian.Uint16(header[8:10]) != protocolMajor ||
			binary.LittleEndian.Uint16(header[10:12]) != RecordHeaderSize {
			return nil, errors.New("invalid record version or header size")
		}
		kind := recordType(binary.LittleEndian.Uint16(header[12:14]))
		if !kind.valid() || binary.LittleEndian.Uint16(header[14:16]) != 0 ||
			binary.LittleEndian.Uint64(header[40:48]) != 0 {
			return nil, errors.New("invalid record type, flags, or reserved field")
		}
		sequence := binary.LittleEndian.Uint64(header[16:24])
		if sequence != expectedSequence {
			return nil, fmt.Errorf("record sequence %d; expected %d", sequence, expectedSequence)
		}
		payloadBytes := binary.LittleEndian.Uint64(header[32:40])
		if payloadBytes > uint64(len(wire)-RecordHeaderSize) {
			return nil, io.ErrUnexpectedEOF
		}
		payloadEnd := RecordHeaderSize + int(payloadBytes)
		payload := wire[RecordHeaderSize:payloadEnd]
		digest := independentRecordDigest(header[:48], payload)
		if !bytes.Equal(header[48:80], digest[:]) {
			return nil, errors.New("record integrity digest mismatch")
		}
		records = append(records, decodedTestRecord{
			kind:      kind,
			sequence:  sequence,
			itemIndex: binary.LittleEndian.Uint64(header[24:32]),
			payload:   bytes.Clone(payload),
		})
		expectedSequence++
		wire = wire[payloadEnd:]
	}
	return records, nil
}

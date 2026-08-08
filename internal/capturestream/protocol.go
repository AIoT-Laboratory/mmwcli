// Package capturestream defines the transport-independent mmwcli capture
// stream wire format. It does not open devices, sockets, or pipes.
package capturestream

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	protocolMajor    uint16 = 1
	RecordHeaderSize        = 80

	MaxSessionPayloadBytes  = 64 << 10
	MaxRadarConfigBytes     = 4 << 20
	MaxFramePayloadBytes    = 64 << 20
	MaxTerminalPayloadBytes = 4 << 10
)

const recordDigestDomain = "mmwcli.capture_stream.record.v1\x00"

var recordMagic = [8]byte{'M', 'M', 'W', 'S', 'T', 'R', 'M', '1'}

type recordType uint16

const (
	recordSession recordType = iota + 1
	recordRadarConfig
	recordFrame
	recordCommit
	recordAbort
)

func (kind recordType) valid() bool {
	return kind >= recordSession && kind <= recordAbort
}

// recordDigest binds the header fields and payload so consumers can detect
// corruption. It is an integrity digest, not peer authentication.
func recordDigest(headerPrefix, payload []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = io.WriteString(hash, recordDigestDomain)
	_, _ = hash.Write(headerPrefix)
	_, _ = hash.Write(payload)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func writeRecord(
	writer io.Writer,
	kind recordType,
	sequence uint64,
	itemIndex uint64,
	payload []byte,
	maximumPayloadBytes int,
) error {
	if writer == nil {
		return errors.New("capture stream writer is nil")
	}
	if !kind.valid() {
		return fmt.Errorf("invalid capture stream record type %d", kind)
	}
	if maximumPayloadBytes < 0 || len(payload) > maximumPayloadBytes {
		return fmt.Errorf(
			"capture stream record payload is %d bytes; limit is %d",
			len(payload),
			maximumPayloadBytes,
		)
	}

	header := make([]byte, RecordHeaderSize)
	copy(header[0:8], recordMagic[:])
	binary.LittleEndian.PutUint16(header[8:10], protocolMajor)
	binary.LittleEndian.PutUint16(header[10:12], RecordHeaderSize)
	binary.LittleEndian.PutUint16(header[12:14], uint16(kind))
	// Bytes 14..15 are flags and 40..47 are reserved. Both remain zero in v1.
	binary.LittleEndian.PutUint64(header[16:24], sequence)
	binary.LittleEndian.PutUint64(header[24:32], itemIndex)
	binary.LittleEndian.PutUint64(header[32:40], uint64(len(payload)))
	digest := recordDigest(header[:48], payload)
	copy(header[48:80], digest[:])

	if err := writeAll(writer, header); err != nil {
		return fmt.Errorf("write capture stream record header: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write capture stream record payload: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

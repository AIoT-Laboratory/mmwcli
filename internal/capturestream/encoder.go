package capturestream

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
)

var (
	ErrEncoderPoisoned = errors.New("capture stream encoder is poisoned")
	ErrEncoderTerminal = errors.New("capture stream encoder already emitted a terminal record")
)

type encoderState uint8

const (
	encoderReady encoderState = iota
	encoderPoisoned
	encoderTerminal
)

// Encoder writes one finite v1 stream. It is single-owner and transport
// independent. NewEncoder emits SESSION and the exact radar configuration;
// frames must then be written once in zero-based order.
type Encoder struct {
	writer        io.Writer
	session       Session
	expectedBytes uint64
	nextFrame     uint64
	adcBytes      uint64
	adcHash       hash.Hash
	state         encoderState
	failure       error
}

func NewEncoder(writer io.Writer, session Session, radarConfig []byte) (*Encoder, error) {
	if writer == nil {
		return nil, errors.New("capture stream writer is nil")
	}
	sessionPayload, expectedBytes, err := buildSessionPayload(session, radarConfig)
	if err != nil {
		return nil, err
	}
	if err := writeRecord(
		writer,
		recordSession,
		0,
		0,
		sessionPayload,
		MaxSessionPayloadBytes,
	); err != nil {
		return nil, err
	}
	if err := writeRecord(
		writer,
		recordRadarConfig,
		1,
		0,
		radarConfig,
		MaxRadarConfigBytes,
	); err != nil {
		return nil, err
	}
	return &Encoder{
		writer:        writer,
		session:       session,
		expectedBytes: expectedBytes,
		adcHash:       sha256.New(),
		state:         encoderReady,
	}, nil
}

func (encoder *Encoder) WriteFrame(index uint64, payload []byte) error {
	if err := encoder.requireReady(); err != nil {
		return err
	}
	if index != encoder.nextFrame {
		return encoder.poison(fmt.Errorf(
			"capture stream frame index is %d; expected %d",
			index,
			encoder.nextFrame,
		))
	}
	if index >= encoder.session.FrameCount {
		return encoder.poison(fmt.Errorf(
			"capture stream frame index %d exceeds finite frame count %d",
			index,
			encoder.session.FrameCount,
		))
	}
	if uint64(len(payload)) != encoder.session.FrameBytes {
		return encoder.poison(fmt.Errorf(
			"capture stream frame %d is %d bytes; expected %d",
			index,
			len(payload),
			encoder.session.FrameBytes,
		))
	}
	if err := writeRecord(
		encoder.writer,
		recordFrame,
		2+index,
		index,
		payload,
		MaxFramePayloadBytes,
	); err != nil {
		return encoder.poison(err)
	}
	written, err := encoder.adcHash.Write(payload)
	if err != nil {
		return encoder.poison(fmt.Errorf("hash capture stream frame %d: %w", index, err))
	}
	if written != len(payload) {
		return encoder.poison(fmt.Errorf("hash capture stream frame %d: %w", index, io.ErrShortWrite))
	}
	encoder.adcBytes += uint64(len(payload))
	encoder.nextFrame++
	return nil
}

// Commit emits COMMIT only when every planned frame was delivered and the
// published session artifact has the same size and logical ADC digest.
func (encoder *Encoder) Commit(artifact Artifact) error {
	if err := encoder.requireReady(); err != nil {
		return err
	}
	if encoder.nextFrame != encoder.session.FrameCount {
		return encoder.poison(fmt.Errorf(
			"capture stream has %d frame(s); expected %d before commit",
			encoder.nextFrame,
			encoder.session.FrameCount,
		))
	}
	if artifact.SizeBytes != encoder.expectedBytes || artifact.SizeBytes != encoder.adcBytes {
		return encoder.poison(fmt.Errorf(
			"capture stream artifact size is %d; expected %d",
			artifact.SizeBytes,
			encoder.expectedBytes,
		))
	}
	digest := encoder.currentDigest()
	if artifact.SHA256 != digest {
		return encoder.poison(errors.New("capture stream artifact SHA-256 does not match emitted frames"))
	}
	payload, err := buildTerminalPayload(
		encoder.session.StreamID,
		"commit",
		encoder.nextFrame,
		encoder.adcBytes,
		digest,
		"",
	)
	if err != nil {
		return encoder.poison(err)
	}
	if err := writeRecord(
		encoder.writer,
		recordCommit,
		2+encoder.nextFrame,
		encoder.nextFrame,
		payload,
		MaxTerminalPayloadBytes,
	); err != nil {
		return encoder.poison(err)
	}
	encoder.state = encoderTerminal
	return nil
}

// Abort emits a best-effort terminal record for a still-writable stream.
// Truncation, EOF, or a missing terminal record remains an abort to consumers.
func (encoder *Encoder) Abort(reason AbortReason) error {
	if err := encoder.requireReady(); err != nil {
		return err
	}
	if err := validateAbortReason(reason); err != nil {
		return encoder.poison(err)
	}
	digest := encoder.currentDigest()
	payload, err := buildTerminalPayload(
		encoder.session.StreamID,
		"abort",
		encoder.nextFrame,
		encoder.adcBytes,
		digest,
		reason,
	)
	if err != nil {
		return encoder.poison(err)
	}
	if err := writeRecord(
		encoder.writer,
		recordAbort,
		2+encoder.nextFrame,
		encoder.nextFrame,
		payload,
		MaxTerminalPayloadBytes,
	); err != nil {
		return encoder.poison(err)
	}
	encoder.state = encoderTerminal
	return nil
}

func (encoder *Encoder) currentDigest() [sha256.Size]byte {
	var digest [sha256.Size]byte
	copy(digest[:], encoder.adcHash.Sum(nil))
	return digest
}

func (encoder *Encoder) requireReady() error {
	if encoder == nil {
		return errors.New("capture stream encoder is nil")
	}
	switch encoder.state {
	case encoderReady:
		return nil
	case encoderPoisoned:
		return fmt.Errorf("%w: %v", ErrEncoderPoisoned, encoder.failure)
	case encoderTerminal:
		return ErrEncoderTerminal
	default:
		return errors.New("capture stream encoder has invalid state")
	}
}

func (encoder *Encoder) poison(err error) error {
	encoder.state = encoderPoisoned
	encoder.failure = err
	return err
}

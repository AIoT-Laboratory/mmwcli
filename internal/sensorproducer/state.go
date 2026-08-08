package sensorproducer

import (
	"errors"
	"fmt"
	"sync"
)

type Phase uint8

const (
	PhaseInit Phase = iota
	PhaseReady
	PhaseArmed
	PhaseStarted
	PhaseStreaming
	PhaseStopped
	PhaseCanceled
	PhaseEnded
	PhaseFailed
	PhaseEOF
	PhaseComplete
)

type ProducerError struct {
	Command Command
	Message string
}

func (failure *ProducerError) Error() string {
	if failure.Command == "" {
		return "sensor producer failed: " + failure.Message
	}
	return fmt.Sprintf("sensor producer rejected %s: %s", failure.Command, failure.Message)
}

type ErrorMetadata struct {
	Message string `json:"message"`
}

type State struct {
	mu sync.Mutex

	sessionID string
	sourceID  string
	phase     Phase
	nextSeq   uint64
	pending   *Control
	lastData  uint64
	cause     error
}

func NewState(sessionID, sourceID string) (*State, error) {
	if err := validateID("session_id", sessionID); err != nil {
		return nil, err
	}
	if err := validateID("source_id", sourceID); err != nil {
		return nil, err
	}
	return &State{sessionID: sessionID, sourceID: sourceID, phase: PhaseInit, nextSeq: 1}, nil
}

func (state *State) Prepare(command Command) (Control, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pending != nil {
		return Control{}, fmt.Errorf("%w: command %s still awaits ACK", ErrProtocol, state.pending.Command)
	}
	if !state.commandAllowed(command) {
		return Control{}, fmt.Errorf("%w: command %s is invalid in phase %d", ErrProtocol, command, state.phase)
	}
	control := Control{
		Version: ProtocolVersion, Command: command, SessionID: state.sessionID,
		SourceID: state.sourceID, Seq: state.nextSeq,
	}
	state.nextSeq++
	state.pending = &control
	return control, nil
}

func (state *State) Accept(record Record) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := record.validate(); err != nil {
		state.fail(err)
		return state.cause
	}
	if record.SessionID != state.sessionID || record.SourceID != state.sourceID {
		state.fail(fmt.Errorf(
			"%w: frame identity %q/%q does not match %q/%q",
			ErrProtocol, record.SessionID, record.SourceID, state.sessionID, state.sourceID,
		))
		return state.cause
	}
	if record.Type == FrameACK {
		return state.acceptACK(record)
	}
	if state.lastData == ^uint64(0) {
		return state.protocolFailure("data frame sequence is exhausted")
	}
	if record.Seq != state.lastData+1 {
		state.fail(fmt.Errorf(
			"%w: data frame sequence %d; expected %d",
			ErrProtocol, record.Seq, state.lastData+1,
		))
		return state.cause
	}
	state.lastData = record.Seq

	switch record.Type {
	case FrameSession:
		if state.phase != PhaseStarted || state.lastData != 1 {
			return state.protocolFailure("SESSION frame is invalid in phase %d", state.phase)
		}
		state.phase = PhaseStreaming
	case FrameItem:
		if state.phase != PhaseStreaming {
			return state.protocolFailure("ITEM frame is invalid in phase %d", state.phase)
		}
	case FrameEnd:
		if state.phase != PhaseStopped {
			return state.protocolFailure("END frame is invalid in phase %d", state.phase)
		}
		state.phase = PhaseEnded
	case FrameError:
		var metadata ErrorMetadata
		if err := decodeStrictJSON(record.Metadata, &metadata); err != nil || metadata.Message == "" || len(metadata.Message) > MaxErrorBytes {
			return state.protocolFailure("ERROR frame metadata is invalid")
		}
		failure := &ProducerError{Message: metadata.Message}
		if state.cause == nil {
			state.cause = failure
		}
		state.phase = PhaseFailed
	case FrameEOF:
		if state.phase != PhaseEnded && state.phase != PhaseCanceled && state.phase != PhaseFailed {
			return state.protocolFailure("EOF frame is invalid in phase %d", state.phase)
		}
		state.phase = PhaseEOF
	default:
		return state.protocolFailure("frame type %d is invalid in the data stream", record.Type)
	}
	return nil
}

func (state *State) AcceptTransportEOF() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != PhaseEOF {
		state.fail(fmt.Errorf("%w: transport EOF arrived in phase %d before an EOF frame", ErrProtocol, state.phase))
		return state.cause
	}
	state.phase = PhaseComplete
	return state.cause
}

func (state *State) Phase() Phase {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.phase
}

func (state *State) commandAllowed(command Command) bool {
	if command == CommandCancel {
		return state.phase >= PhaseInit && state.phase <= PhaseStreaming
	}
	switch state.phase {
	case PhaseInit:
		return command == CommandReady
	case PhaseReady:
		return command == CommandArm
	case PhaseArmed:
		return command == CommandStart
	case PhaseStarted, PhaseStreaming:
		return command == CommandStop
	default:
		return false
	}
}

func (state *State) acceptACK(record Record) error {
	if state.pending == nil {
		return state.protocolFailure("unsolicited ACK sequence %d", record.Seq)
	}
	pending := *state.pending
	if record.Seq != pending.Seq {
		return state.protocolFailure("ACK sequence %d; expected %d", record.Seq, pending.Seq)
	}
	metadata, err := DecodeACK(record)
	if err != nil {
		state.fail(err)
		return err
	}
	if metadata.Command != pending.Command {
		return state.protocolFailure("ACK command %s; expected %s", metadata.Command, pending.Command)
	}
	state.pending = nil
	if !metadata.OK {
		failure := &ProducerError{Command: pending.Command, Message: metadata.Error}
		state.fail(failure)
		return failure
	}
	switch pending.Command {
	case CommandReady:
		state.phase = PhaseReady
	case CommandArm:
		state.phase = PhaseArmed
	case CommandStart:
		state.phase = PhaseStarted
	case CommandStop:
		state.phase = PhaseStopped
	case CommandCancel:
		state.phase = PhaseCanceled
	default:
		return state.protocolFailure("ACK command %s has no transition", pending.Command)
	}
	return nil
}

func (state *State) protocolFailure(format string, arguments ...any) error {
	failure := fmt.Errorf("%w: %s", ErrProtocol, fmt.Sprintf(format, arguments...))
	state.fail(failure)
	return failure
}

func (state *State) fail(err error) {
	if err == nil {
		return
	}
	if state.cause == nil || errors.Is(state.cause, ErrProtocol) {
		state.cause = err
	}
	state.phase = PhaseFailed
}

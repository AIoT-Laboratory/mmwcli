package sensorproducer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestControlJSONLCodecIsStrictAndBounded(t *testing.T) {
	want := Control{
		Version: ProtocolVersion, Command: CommandArm,
		SessionID: "session-01", SourceID: "camera:left", Seq: 2,
	}
	var wire bytes.Buffer
	encoder, err := NewControlEncoder(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Write(want); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(wire.Bytes(), []byte{'\n'}) || bytes.Contains(wire.Bytes(), []byte("payload")) {
		t.Fatalf("control wire = %q", wire.Bytes())
	}
	decoder, err := NewControlDecoder(&wire)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decoder.Read()
	if err != nil || got != want {
		t.Fatalf("decoded control = %+v, %v", got, err)
	}

	unknown := strings.NewReader(`{"version":1,"command":"READY","session_id":"s","source_id":"c","seq":1,"extra":true}` + "\n")
	decoder, _ = NewControlDecoder(unknown)
	if _, err := decoder.Read(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unknown-field error = %v", err)
	}
	tooLong := strings.NewReader(strings.Repeat("x", MaxControlBytes+1) + "\n")
	decoder, _ = NewControlDecoder(tooLong)
	if _, err := decoder.Read(); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized control error = %v", err)
	}
}

func TestBinaryFrameCodecPreservesRawPayloadAndDigest(t *testing.T) {
	want := Record{
		Type: FrameItem, SessionID: "session-01", SourceID: "camera.left", Seq: 7,
		Metadata: []byte(`{"timestamp_ns":123,"format":"gray8"}`),
		Payload:  []byte{0, 1, 0, 0xff, '\n', '{', '}'},
	}
	var wire bytes.Buffer
	encoder, err := NewEncoder(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Write(want); err != nil {
		t.Fatal(err)
	}
	encoded := append([]byte(nil), wire.Bytes()...)
	if string(encoded[:8]) != FrameMagic || binary.LittleEndian.Uint16(encoded[8:10]) != ProtocolVersion ||
		binary.LittleEndian.Uint64(encoded[32:40]) != uint64(len(want.Payload)) {
		t.Fatalf("frame header = % X", encoded[:frameHeaderSize])
	}
	decoder, _ := NewDecoder(bytes.NewReader(encoded))
	got, err := decoder.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.SessionID != want.SessionID || got.SourceID != want.SourceID || got.Seq != want.Seq ||
		!bytes.Equal(got.Metadata, want.Metadata) || !slices.Equal(got.Payload, want.Payload) {
		t.Fatalf("decoded record = %+v", got)
	}
	if _, err := decoder.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal decoder error = %v", err)
	}

	corrupt := append([]byte(nil), encoded...)
	corrupt[len(corrupt)-1] ^= 0xff
	decoder, _ = NewDecoder(bytes.NewReader(corrupt))
	if _, err := decoder.Read(); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("corrupt digest error = %v", err)
	}

	header := make([]byte, frameHeaderSize)
	copy(header[:8], FrameMagic)
	binary.LittleEndian.PutUint16(header[8:10], ProtocolVersion)
	binary.LittleEndian.PutUint16(header[10:12], uint16(FrameItem))
	binary.LittleEndian.PutUint64(header[16:24], 1)
	binary.LittleEndian.PutUint16(header[24:26], 1)
	binary.LittleEndian.PutUint16(header[26:28], 1)
	binary.LittleEndian.PutUint32(header[28:32], 2)
	binary.LittleEndian.PutUint64(header[32:40], MaxPayloadBytes+1)
	decoder, _ = NewDecoder(bytes.NewReader(header))
	if _, err := decoder.Read(); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized frame error = %v", err)
	}

	invalid := want
	invalid.Type = FrameSession
	if err := encoder.Write(invalid); !errors.Is(err, ErrProtocol) {
		t.Fatalf("non-item payload error = %v", err)
	}
}

func TestStateEnforcesControlACKAndDataSequence(t *testing.T) {
	state, err := NewState("session-01", "camera.left")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []Command{CommandReady, CommandArm, CommandStart} {
		acceptSuccessfulACK(t, state, command)
	}
	if state.Phase() != PhaseStarted {
		t.Fatalf("phase after START = %d", state.Phase())
	}
	for _, record := range []Record{
		{Type: FrameSession, SessionID: "session-01", SourceID: "camera.left", Seq: 1, Metadata: []byte(`{"clock":"monotonic"}`)},
		{Type: FrameItem, SessionID: "session-01", SourceID: "camera.left", Seq: 2, Metadata: []byte(`{"index":0}`), Payload: []byte{0, 0xff}},
	} {
		if err := state.Accept(record); err != nil {
			t.Fatal(err)
		}
	}
	acceptSuccessfulACK(t, state, CommandStop)
	for _, record := range []Record{
		{Type: FrameEnd, SessionID: "session-01", SourceID: "camera.left", Seq: 3, Metadata: []byte(`{"items":1}`)},
		{Type: FrameEOF, SessionID: "session-01", SourceID: "camera.left", Seq: 4, Metadata: []byte(`{}`)},
	} {
		if err := state.Accept(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.AcceptTransportEOF(); err != nil || state.Phase() != PhaseComplete {
		t.Fatalf("transport EOF = %v, phase=%d", err, state.Phase())
	}

	mismatch, _ := NewState("session-02", "camera.left")
	control, err := mismatch.Prepare(CommandReady)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := NewACKRecord(control, true, "")
	if err != nil {
		t.Fatal(err)
	}
	ack.Seq++
	if err := mismatch.Accept(ack); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "ACK sequence") {
		t.Fatalf("mismatched ACK error = %v", err)
	}

	sequence, _ := NewState("session-03", "camera.left")
	for _, command := range []Command{CommandReady, CommandArm, CommandStart} {
		acceptSuccessfulACK(t, sequence, command)
	}
	bad := Record{Type: FrameSession, SessionID: "session-03", SourceID: "camera.left", Seq: 2, Metadata: []byte(`{}`)}
	if err := sequence.Accept(bad); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("data sequence error = %v", err)
	}
}

func acceptSuccessfulACK(t *testing.T, state *State, command Command) {
	t.Helper()
	control, err := state.Prepare(command)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := NewACKRecord(control, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Accept(ack); err != nil {
		t.Fatal(err)
	}
}

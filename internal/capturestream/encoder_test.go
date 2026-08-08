package capturestream

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"mmwcli/internal/radar"
)

const goldenV1Hex = "4d4d575354524d31010050000100000000000000000000000000000000000000bd0200000000000000000000000000000c472b63a5169b7cd8cd592237c781464bdbd38d2d13126aae57e8a2358dac3b7b22736368656d61223a226d6d77636c692e636170747572655f73747265616d2e7631222c2273747265616d5f6964223a223030303130323033303430353036303730383039306130623063306430653066222c2270726f6475636572223a7b226e616d65223a226d6d77636c69222c2276657273696f6e223a2274657374227d2c226d6f6465223a2273747564696f2d636c69222c226861726477617265223a7b2276656e646f72223a227469222c2266616d696c79223a2278777236387878222c226d6f64656c223a22222c227265766973696f6e223a22222c226964656e746974795f736f75726365223a22726f7574655f6465636c61726174696f6e227d2c2263617074757265223a7b226672616d655f636f756e74223a312c226672616d655f6279746573223a36342c2265787065637465645f6279746573223a36342c227265636f72645f73657175656e63655f6f726967696e223a302c226672616d655f696e6465785f6f726967696e223a302c226164635f627974655f6f66667365745f6f726967696e223a307d2c22616463223a7b226474797065223a22696e743136222c22627974655f6f72646572223a226c6974746c65222c226c616e655f636f756e74223a322c226c61796f7574223a2267726f7570325f695f7468656e5f71227d2c2272616461725f636f6e666967223a7b22666f726d6174223a2274695f6d6d776176655f6c65676163795f636c692e7631222c2273697a655f6279746573223a3232352c22736861323536223a2262396565626638613466656534303239353936333863373232633964613539353734386665383938373537393664613034343466333262316339656535646562227d2c226172746966616374223a7b227265717569726564223a747275652c22736368656d61223a226d6d77636c692e636170747572655f73657373696f6e2e7631227d7d0a4d4d575354524d31010050000200000001000000000000000000000000000000e10000000000000000000000000000002ed2891e73145ea55d3fe68f550123e3a1801c02875291d3581df8196e9eebeb666c7573684366670a646665446174614f75747075744d6f646520310a6368616e6e656c4366672031203120300a616463436667203220310a616463627566436667202d3120302031203120310a70726f66696c654366672030203630203720332032342030203020313636203120313620313235303020302030203135380a6368697270436667203020302030203020302030203020310a6672616d654366672030203020312031203130203120300a6c6f77506f776572203020300a6c76647353747265616d436667202d312030203120300a73656e736f7253746172740a4d4d575354524d310100500003000000020000000000000000000000000000004000000000000000000000000000000014cae892709325bd9f8e84a1d832feaf9589b669fdb32a1375d368524b606d00000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f4d4d575354524d31010050000400000003000000000000000100000000000000db0000000000000000000000000000001157156605f2c9d58b5c7e23256e6753337676f79ccc801728c06a1dc5486e1d7b22736368656d61223a226d6d77636c692e636170747572655f73747265616d5f7465726d696e616c2e7631222c2273747265616d5f6964223a223030303130323033303430353036303730383039306130623063306430653066222c226f7574636f6d65223a22636f6d6d6974222c226672616d6573223a312c226164635f6279746573223a36342c226164635f736861323536223a2266646561623961636633373130333632626432363538636463396132396538663963373537666366393831313630336138633434376364316439313531313038227d0a"

const goldenRadarConfig = "flushCfg\n" +
	"dfeDataOutputMode 1\n" +
	"channelCfg 1 1 0\n" +
	"adcCfg 2 1\n" +
	"adcbufCfg -1 0 1 1 1\n" +
	"profileCfg 0 60 7 3 24 0 0 166 1 16 12500 0 0 158\n" +
	"chirpCfg 0 0 0 0 0 0 0 1\n" +
	"frameCfg 0 0 1 1 10 1 0\n" +
	"lowPower 0 0\n" +
	"lvdsStreamCfg -1 0 1 0\n" +
	"sensorStart\n"

func TestEncoderWritesGoldenFiniteCommitStream(t *testing.T) {
	session := testSession()
	config := []byte(goldenRadarConfig)
	plan, err := radar.BuildCaptureSessionV1Plan(config, radar.FullConfiguration)
	if err != nil {
		t.Fatalf("golden radar configuration is invalid: %v", err)
	}
	if plan.BytesPerFrame != 64 || plan.NumberOfFrames != 1 || plan.ExpectedBytes != 64 {
		t.Fatalf("golden radar plan = %+v", plan)
	}
	frame := make([]byte, 64)
	for index := range frame {
		frame[index] = byte(index)
	}
	var stream bytes.Buffer
	encoder, err := NewEncoder(&stream, session, plan, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteFrame(0, frame); err != nil {
		t.Fatal(err)
	}
	frameDigest := sha256.Sum256(frame)
	if err := encoder.Commit(Artifact{sizeBytes: uint64(len(frame)), sha256: frameDigest}); err != nil {
		t.Fatal(err)
	}

	records, err := decodeTestRecords(stream.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("record count = %d", len(records))
	}
	wantTypes := []recordType{recordSession, recordRadarConfig, recordFrame, recordCommit}
	for index, record := range records {
		if record.kind != wantTypes[index] {
			t.Fatalf("record %d type = %d", index, record.kind)
		}
	}
	if records[0].itemIndex != 0 || records[1].itemIndex != 0 ||
		records[2].itemIndex != 0 || records[3].itemIndex != 1 {
		t.Fatalf("record item indices = %d,%d,%d,%d",
			records[0].itemIndex,
			records[1].itemIndex,
			records[2].itemIndex,
			records[3].itemIndex,
		)
	}
	var header sessionRecordV1
	if err := json.Unmarshal(records[0].payload, &header); err != nil {
		t.Fatal(err)
	}
	configDigest := sha256.Sum256(config)
	if header.Schema != SchemaV1 ||
		header.StreamID != streamIDString(session.StreamID) ||
		header.Producer != (producerRecordV1{Name: producerName, Version: "test"}) ||
		header.Mode != string(CaptureModeStudioCLI) ||
		header.Hardware != (hardwareRecordV1{
			Vendor:         "ti",
			Family:         "xwr68xx",
			Model:          "",
			Revision:       "",
			IdentitySource: "route_declaration",
		}) ||
		header.Capture.FrameCount != 1 ||
		header.Capture.FrameBytes != 64 ||
		header.Capture.ExpectedBytes != 64 ||
		header.Capture.RecordSequenceOrigin != 0 ||
		header.Capture.FrameIndexOrigin != 0 ||
		header.Capture.ADCByteOffsetOrigin != 0 ||
		header.ADC != (adcRecordV1{
			DataType:  "int16",
			ByteOrder: "little",
			LaneCount: 2,
			Layout:    "group2_i_then_q",
		}) ||
		header.RadarConfig != (radarConfigRecordV1{
			Format:    "ti_mmwave_legacy_cli.v1",
			SizeBytes: uint64(len(config)),
			SHA256:    hex.EncodeToString(configDigest[:]),
		}) ||
		header.Artifact != (artifactRecordV1{Required: true, Schema: CaptureSessionSchemaV1}) {
		t.Fatalf("session header = %+v", header)
	}
	if !bytes.Equal(records[1].payload, config) || !bytes.Equal(records[2].payload, frame) {
		t.Fatal("configuration or frame payload changed")
	}
	var terminal terminalRecordV1
	if err := json.Unmarshal(records[3].payload, &terminal); err != nil {
		t.Fatal(err)
	}
	var terminalWire map[string]json.RawMessage
	if err := json.Unmarshal(records[3].payload, &terminalWire); err != nil {
		t.Fatal(err)
	}
	if len(terminalWire) != 6 {
		t.Fatalf("commit terminal keys = %v", terminalWire)
	}
	if _, found := terminalWire["reason_code"]; found {
		t.Fatal("commit terminal contains reason_code")
	}
	if terminal.Schema != TerminalSchemaV1 ||
		terminal.StreamID != header.StreamID ||
		terminal.Outcome != "commit" ||
		terminal.Frames != 1 ||
		terminal.ADCBytes != 64 ||
		terminal.ADCSHA256 != hex.EncodeToString(frameDigest[:]) ||
		terminal.ReasonCode != "" {
		t.Fatalf("terminal = %+v", terminal)
	}

	gotHex := hex.EncodeToString(stream.Bytes())
	if gotHex != goldenV1Hex {
		t.Fatalf("golden stream mismatch\ngot: %s\nwant: %s", gotHex, goldenV1Hex)
	}
}

func TestEncoderAcceptsDebugCLIMode(t *testing.T) {
	session := testSession()
	session.Mode = CaptureModeDebugCLI
	plan, config := testCapturePlan(t, 1)
	var stream bytes.Buffer
	if _, err := NewEncoder(&stream, session, plan, config); err != nil {
		t.Fatal(err)
	}
	records, err := decodeTestRecords(stream.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("initial record count = %d, want 2", len(records))
	}
	var header sessionRecordV1
	if err := json.Unmarshal(records[0].payload, &header); err != nil {
		t.Fatal(err)
	}
	if header.Mode != string(CaptureModeDebugCLI) {
		t.Fatalf("session mode = %q, want %q", header.Mode, CaptureModeDebugCLI)
	}
}

func TestEncoderWritesAbortForProvisionalFrames(t *testing.T) {
	session := testSession()
	plan, config := testCapturePlan(t, 2)
	var stream bytes.Buffer
	encoder, err := NewEncoder(&stream, session, plan, config)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 64)
	frame[0] = 1
	if err := encoder.WriteFrame(0, frame); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Abort(AbortCancelled); err != nil {
		t.Fatal(err)
	}
	records, err := decodeTestRecords(stream.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[3].kind != recordAbort || records[3].itemIndex != 1 {
		t.Fatalf("abort records = %+v", records)
	}
	var terminal terminalRecordV1
	if err := json.Unmarshal(records[3].payload, &terminal); err != nil {
		t.Fatal(err)
	}
	var terminalWire map[string]json.RawMessage
	if err := json.Unmarshal(records[3].payload, &terminalWire); err != nil {
		t.Fatal(err)
	}
	if len(terminalWire) != 7 {
		t.Fatalf("abort terminal keys = %v", terminalWire)
	}
	if _, found := terminalWire["reason_code"]; !found {
		t.Fatal("abort terminal omits reason_code")
	}
	digest := sha256.Sum256(frame)
	if terminal.Outcome != "abort" ||
		terminal.ReasonCode != string(AbortCancelled) ||
		terminal.Frames != 1 ||
		terminal.ADCBytes != 64 ||
		terminal.ADCSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("abort terminal = %+v", terminal)
	}
	if err := encoder.WriteFrame(1, frame); !errors.Is(err, ErrEncoderTerminal) {
		t.Fatalf("write after abort error = %v", err)
	}
}

func TestEncoderPoisonsInvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Encoder) error
	}{
		{
			name: "wrong frame index",
			run:  func(encoder *Encoder) error { return encoder.WriteFrame(1, make([]byte, 64)) },
		},
		{
			name: "wrong frame size",
			run:  func(encoder *Encoder) error { return encoder.WriteFrame(0, make([]byte, 2)) },
		},
		{
			name: "early commit",
			run:  func(encoder *Encoder) error { return encoder.Commit(Artifact{}) },
		},
		{
			name: "unknown abort reason",
			run:  func(encoder *Encoder) error { return encoder.Abort("unknown") },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, config := testCapturePlan(t, 1)
			var stream bytes.Buffer
			encoder, err := NewEncoder(&stream, testSession(), plan, config)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.run(encoder); err == nil {
				t.Fatal("invalid transition succeeded")
			}
			if err := encoder.Abort(AbortCaptureFailed); !errors.Is(err, ErrEncoderPoisoned) {
				t.Fatalf("poisoned encoder error = %v", err)
			}
		})
	}
}

func TestEncoderRejectsArtifactMismatchAndSecondTerminal(t *testing.T) {
	plan, config := testCapturePlan(t, 1)
	frame := make([]byte, 64)
	frame[0] = 1
	var mismatchStream bytes.Buffer
	mismatch, err := NewEncoder(&mismatchStream, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := mismatch.WriteFrame(0, frame); err != nil {
		t.Fatal(err)
	}
	if err := mismatch.Commit(Artifact{sizeBytes: 64}); err == nil {
		t.Fatal("mismatched artifact digest was accepted")
	}
	if err := mismatch.Abort(AbortIntegrityFailed); !errors.Is(err, ErrEncoderPoisoned) {
		t.Fatalf("mismatched encoder state = %v", err)
	}

	var committedStream bytes.Buffer
	committed, err := NewEncoder(&committedStream, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := committed.WriteFrame(0, frame); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(frame)
	if err := committed.Commit(Artifact{sizeBytes: 64, sha256: digest}); err != nil {
		t.Fatal(err)
	}
	if err := committed.Abort(AbortCancelled); !errors.Is(err, ErrEncoderTerminal) {
		t.Fatalf("second terminal error = %v", err)
	}
}

func TestNewEncoderValidatesCompleteContractBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Session, *radar.CapturePlan, *[]byte)
	}{
		{
			name: "zero stream id",
			mutate: func(session *Session, _ *radar.CapturePlan, _ *[]byte) {
				session.StreamID = [16]byte{}
			},
		},
		{
			name: "blank version",
			mutate: func(session *Session, _ *radar.CapturePlan, _ *[]byte) {
				session.ProducerVersion = " "
			},
		},
		{
			name: "unknown mode",
			mutate: func(session *Session, _ *radar.CapturePlan, _ *[]byte) {
				session.Mode = "custom"
			},
		},
		{
			name: "removed debug-capture mode",
			mutate: func(session *Session, _ *radar.CapturePlan, _ *[]byte) {
				session.Mode = "debug-capture"
			},
		},
		{
			name: "missing raw capture contract",
			mutate: func(_ *Session, plan *radar.CapturePlan, _ *[]byte) {
				plan.RawCapture = radar.RawCaptureContract{}
			},
		},
		{
			name: "zero frames in supplied plan",
			mutate: func(_ *Session, plan *radar.CapturePlan, _ *[]byte) {
				plan.NumberOfFrames = 0
			},
		},
		{
			name: "unaligned frame in supplied plan",
			mutate: func(_ *Session, plan *radar.CapturePlan, _ *[]byte) {
				plan.BytesPerFrame = 3
			},
		},
		{
			name: "different physical command with equal geometry",
			mutate: func(_ *Session, _ *radar.CapturePlan, config *[]byte) {
				*config = []byte(strings.Replace(
					string(*config),
					"profileCfg 0 60",
					"profileCfg 0 61",
					1,
				))
			},
		},
		{
			name: "incomplete config",
			mutate: func(_ *Session, _ *radar.CapturePlan, config *[]byte) {
				*config = []byte("flushCfg\n")
			},
		},
		{
			name: "empty config",
			mutate: func(_ *Session, _ *radar.CapturePlan, config *[]byte) {
				*config = nil
			},
		},
		{
			name: "invalid UTF-8 config",
			mutate: func(_ *Session, _ *radar.CapturePlan, config *[]byte) {
				*config = []byte{0xff}
			},
		},
		{
			name: "oversize config",
			mutate: func(_ *Session, _ *radar.CapturePlan, config *[]byte) {
				*config = make([]byte, MaxRadarConfigBytes+1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := testSession()
			plan, config := testCapturePlan(t, 1)
			test.mutate(&session, &plan, &config)
			var stream bytes.Buffer
			if _, err := NewEncoder(&stream, session, plan, config); err == nil {
				t.Fatal("invalid contract was accepted")
			}
			if stream.Len() != 0 {
				t.Fatalf("invalid contract wrote %d bytes", stream.Len())
			}
		})
	}
}

func TestCaptureShapeEnforcesStreamBounds(t *testing.T) {
	oversized := int64(MaxFramePayloadBytes) + 2
	tests := []struct {
		name string
		plan radar.CapturePlan
	}{
		{
			name: "zero frames",
			plan: radar.CapturePlan{BytesPerFrame: 64},
		},
		{
			name: "negative frame bytes",
			plan: radar.CapturePlan{NumberOfFrames: 1, BytesPerFrame: -2},
		},
		{
			name: "unaligned frame bytes",
			plan: radar.CapturePlan{NumberOfFrames: 1, BytesPerFrame: 3, ExpectedBytes: 3},
		},
		{
			name: "oversized frame bytes",
			plan: radar.CapturePlan{
				NumberOfFrames: 1,
				BytesPerFrame:  oversized,
				ExpectedBytes:  oversized,
			},
		},
		{
			name: "mismatched expected bytes",
			plan: radar.CapturePlan{NumberOfFrames: 2, BytesPerFrame: 64, ExpectedBytes: 64},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := captureShapeFromPlan(test.plan); err == nil {
				t.Fatal("invalid capture shape was accepted")
			}
		})
	}

	frameCount := uint16(math.MaxUint16)
	frameBytes := int64(MaxFramePayloadBytes)
	expectedBytes := int64(frameCount) * frameBytes
	shape, err := captureShapeFromPlan(radar.CapturePlan{
		NumberOfFrames: frameCount,
		BytesPerFrame:  frameBytes,
		ExpectedBytes:  expectedBytes,
	})
	if err != nil {
		t.Fatalf("maximum bounded capture shape: %v", err)
	}
	if shape.frameCount != uint64(frameCount) ||
		shape.frameBytes != uint64(frameBytes) ||
		shape.expectedBytes != uint64(expectedBytes) {
		t.Fatalf("maximum bounded capture shape = %+v", shape)
	}
}

func TestEncoderWriteFailurePoisonsState(t *testing.T) {
	plan, config := testCapturePlan(t, 1)
	writer := &budgetWriter{remaining: math.MaxInt}
	encoder, err := NewEncoder(writer, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	writer.remaining = 10
	if err := encoder.WriteFrame(0, make([]byte, 64)); !errors.Is(err, errBudgetExhausted) {
		t.Fatalf("frame write error = %v", err)
	}
	if err := encoder.Abort(AbortCaptureFailed); !errors.Is(err, ErrEncoderPoisoned) {
		t.Fatalf("poisoned encoder error = %v", err)
	}
}

func TestNewStreamIDIsNonZero(t *testing.T) {
	identifier, err := NewStreamID()
	if err != nil {
		t.Fatal(err)
	}
	if identifier == ([16]byte{}) {
		t.Fatal("random stream identifier is zero")
	}
}

func testSession() Session {
	return Session{
		StreamID: [16]byte{
			0x00, 0x01, 0x02, 0x03,
			0x04, 0x05, 0x06, 0x07,
			0x08, 0x09, 0x0a, 0x0b,
			0x0c, 0x0d, 0x0e, 0x0f,
		},
		ProducerVersion: "test",
		Mode:            CaptureModeStudioCLI,
	}
}

func testCapturePlan(t *testing.T, frameCount uint16) (radar.CapturePlan, []byte) {
	t.Helper()
	config := []byte(strings.Replace(
		goldenRadarConfig,
		"frameCfg 0 0 1 1 10 1 0",
		fmt.Sprintf("frameCfg 0 0 1 %d 10 1 0", frameCount),
		1,
	))
	plan, err := radar.BuildCaptureSessionV1Plan(config, radar.FullConfiguration)
	if err != nil {
		t.Fatalf("build test capture plan: %v", err)
	}
	return plan, config
}

var errBudgetExhausted = errors.New("writer budget exhausted")

type budgetWriter struct {
	bytes.Buffer
	remaining int
}

func (writer *budgetWriter) Write(data []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, errBudgetExhausted
	}
	if len(data) > writer.remaining {
		data = data[:writer.remaining]
		written, _ := writer.Buffer.Write(data)
		writer.remaining -= written
		return written, errBudgetExhausted
	}
	written, err := writer.Buffer.Write(data)
	writer.remaining -= written
	return written, err
}

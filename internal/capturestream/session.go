package capturestream

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"mmwcli/internal/radar"
)

const (
	SchemaV1               = "mmwcli.capture_stream.v1"
	TerminalSchemaV1       = "mmwcli.capture_stream_terminal.v1"
	CaptureSessionSchemaV1 = "mmwcli.capture_session.v1"

	producerName       = "mmwcli"
	adcDataType        = "int16"
	adcByteOrder       = "little"
	adcLayout          = "group2_i_then_q"
	radarConfigFormat  = "ti_xwr68xx_legacy_cli"
	maximumVersionSize = 128
)

type CaptureMode string

const (
	CaptureModeStudioCLI    CaptureMode = "studio-cli"
	CaptureModeDebugCapture CaptureMode = "debug-capture"
)

// Session is immutable producer metadata. NewEncoder combines it with an exact
// CFG-bound radar plan before writing the finite capture contract. StreamID is
// correlation metadata; it does not authenticate a peer.
type Session struct {
	StreamID        [16]byte
	ProducerVersion string
	Mode            CaptureMode
}

type captureShape struct {
	frameCount    uint64
	frameBytes    uint64
	expectedBytes uint64
}

// Artifact is the already-published capture-session ADC evidence checked
// before a stream may emit COMMIT.
type Artifact struct {
	SizeBytes uint64
	SHA256    [sha256.Size]byte
}

type AbortReason string

const (
	AbortCancelled       AbortReason = "cancelled"
	AbortBackpressure    AbortReason = "backpressure"
	AbortCaptureFailed   AbortReason = "capture_failed"
	AbortIntegrityFailed AbortReason = "integrity_failed"
	AbortCleanupFailed   AbortReason = "cleanup_failed"
	AbortPublishFailed   AbortReason = "publish_failed"
)

type sessionRecordV1 struct {
	Schema      string              `json:"schema"`
	StreamID    string              `json:"stream_id"`
	Producer    producerRecordV1    `json:"producer"`
	Mode        string              `json:"mode"`
	Capture     captureRecordV1     `json:"capture"`
	ADC         adcRecordV1         `json:"adc"`
	RadarConfig radarConfigRecordV1 `json:"radar_config"`
	Artifact    artifactRecordV1    `json:"artifact"`
}

type producerRecordV1 struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type captureRecordV1 struct {
	FrameCount           uint64 `json:"frame_count"`
	FrameBytes           uint64 `json:"frame_bytes"`
	ExpectedBytes        uint64 `json:"expected_bytes"`
	RecordSequenceOrigin uint64 `json:"record_sequence_origin"`
	FrameIndexOrigin     uint64 `json:"frame_index_origin"`
	ADCByteOffsetOrigin  uint64 `json:"adc_byte_offset_origin"`
}

type adcRecordV1 struct {
	DataType  string `json:"dtype"`
	ByteOrder string `json:"byte_order"`
	Layout    string `json:"layout"`
}

type radarConfigRecordV1 struct {
	Format    string `json:"format"`
	SizeBytes uint64 `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type artifactRecordV1 struct {
	Required bool   `json:"required"`
	Schema   string `json:"schema"`
}

type terminalRecordV1 struct {
	Schema     string `json:"schema"`
	StreamID   string `json:"stream_id"`
	Outcome    string `json:"outcome"`
	Frames     uint64 `json:"frames"`
	ADCBytes   uint64 `json:"adc_bytes"`
	ADCSHA256  string `json:"adc_sha256"`
	ReasonCode string `json:"reason_code,omitempty"`
}

func NewStreamID() ([16]byte, error) {
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return identifier, fmt.Errorf("create capture stream identifier: %w", err)
	}
	return identifier, nil
}

func buildSessionPayload(
	session Session,
	plan radar.CapturePlan,
	radarConfig []byte,
) ([]byte, captureShape, error) {
	shape, err := validateSession(session, plan, radarConfig)
	if err != nil {
		return nil, captureShape{}, err
	}
	configDigest := sha256.Sum256(radarConfig)
	record := sessionRecordV1{
		Schema:   SchemaV1,
		StreamID: streamIDString(session.StreamID),
		Producer: producerRecordV1{
			Name:    producerName,
			Version: session.ProducerVersion,
		},
		Mode: string(session.Mode),
		Capture: captureRecordV1{
			FrameCount:           shape.frameCount,
			FrameBytes:           shape.frameBytes,
			ExpectedBytes:        shape.expectedBytes,
			RecordSequenceOrigin: 0,
			FrameIndexOrigin:     0,
			ADCByteOffsetOrigin:  0,
		},
		ADC: adcRecordV1{
			DataType:  adcDataType,
			ByteOrder: adcByteOrder,
			Layout:    adcLayout,
		},
		RadarConfig: radarConfigRecordV1{
			Format:    radarConfigFormat,
			SizeBytes: uint64(len(radarConfig)),
			SHA256:    hex.EncodeToString(configDigest[:]),
		},
		Artifact: artifactRecordV1{
			Required: true,
			Schema:   CaptureSessionSchemaV1,
		},
	}
	payload, err := encodeJSONLine(record)
	if err != nil {
		return nil, captureShape{}, fmt.Errorf("encode capture stream session: %w", err)
	}
	if len(payload) > MaxSessionPayloadBytes {
		return nil, captureShape{}, fmt.Errorf(
			"capture stream session header is %d bytes; limit is %d",
			len(payload),
			MaxSessionPayloadBytes,
		)
	}
	return payload, shape, nil
}

func validateSession(
	session Session,
	plan radar.CapturePlan,
	radarConfig []byte,
) (captureShape, error) {
	if session.StreamID == ([16]byte{}) {
		return captureShape{}, errors.New("capture stream identifier must not be zero")
	}
	if err := validateProducerVersion(session.ProducerVersion); err != nil {
		return captureShape{}, err
	}
	if session.Mode != CaptureModeStudioCLI && session.Mode != CaptureModeDebugCapture {
		return captureShape{}, fmt.Errorf("unsupported capture stream mode %q", session.Mode)
	}
	if len(radarConfig) == 0 || len(bytes.TrimSpace(radarConfig)) == 0 {
		return captureShape{}, errors.New("capture stream radar configuration is empty")
	}
	if len(radarConfig) > MaxRadarConfigBytes {
		return captureShape{}, fmt.Errorf(
			"capture stream radar configuration is %d bytes; limit is %d",
			len(radarConfig),
			MaxRadarConfigBytes,
		)
	}
	if !utf8.Valid(radarConfig) {
		return captureShape{}, errors.New("capture stream radar configuration is not valid UTF-8")
	}
	if err := radar.ValidateCaptureSessionV1Plan(radarConfig, plan); err != nil {
		return captureShape{}, fmt.Errorf("bind capture stream to radar plan: %w", err)
	}
	return captureShapeFromPlan(plan)
}

func captureShapeFromPlan(plan radar.CapturePlan) (captureShape, error) {
	if plan.NumberOfFrames == 0 {
		return captureShape{}, errors.New("capture stream frame count must be positive")
	}
	if plan.BytesPerFrame <= 0 || plan.BytesPerFrame%2 != 0 {
		return captureShape{}, errors.New("capture stream frame size must be positive and aligned to int16")
	}
	if plan.BytesPerFrame > int64(MaxFramePayloadBytes) {
		return captureShape{}, fmt.Errorf(
			"capture stream frame size %d exceeds limit %d",
			plan.BytesPerFrame,
			MaxFramePayloadBytes,
		)
	}
	frameCount := uint64(plan.NumberOfFrames)
	frameBytes := uint64(plan.BytesPerFrame)
	expectedBytes := frameCount * frameBytes
	if plan.ExpectedBytes <= 0 || uint64(plan.ExpectedBytes) != expectedBytes {
		return captureShape{}, errors.New("capture stream expected byte count does not match its finite frame shape")
	}
	return captureShape{
		frameCount:    frameCount,
		frameBytes:    frameBytes,
		expectedBytes: expectedBytes,
	}, nil
}

func validateProducerVersion(version string) error {
	if version == "" || strings.TrimSpace(version) != version {
		return errors.New("capture stream producer version must be non-empty without surrounding whitespace")
	}
	if !utf8.ValidString(version) || len(version) > maximumVersionSize {
		return fmt.Errorf(
			"capture stream producer version must be valid UTF-8 within %d bytes",
			maximumVersionSize,
		)
	}
	for _, character := range version {
		if unicode.IsControl(character) {
			return errors.New("capture stream producer version must not contain control characters")
		}
	}
	return nil
}

func validateAbortReason(reason AbortReason) error {
	switch reason {
	case AbortCancelled,
		AbortBackpressure,
		AbortCaptureFailed,
		AbortIntegrityFailed,
		AbortCleanupFailed,
		AbortPublishFailed:
		return nil
	default:
		return fmt.Errorf("unsupported capture stream abort reason %q", reason)
	}
}

func buildTerminalPayload(
	streamID [16]byte,
	outcome string,
	frames uint64,
	adcBytes uint64,
	adcDigest [sha256.Size]byte,
	reason AbortReason,
) ([]byte, error) {
	record := terminalRecordV1{
		Schema:    TerminalSchemaV1,
		StreamID:  streamIDString(streamID),
		Outcome:   outcome,
		Frames:    frames,
		ADCBytes:  adcBytes,
		ADCSHA256: hex.EncodeToString(adcDigest[:]),
	}
	if outcome == "abort" {
		if err := validateAbortReason(reason); err != nil {
			return nil, err
		}
		record.ReasonCode = string(reason)
	} else if outcome != "commit" || reason != "" {
		return nil, errors.New("invalid capture stream terminal outcome")
	}
	payload, err := encodeJSONLine(record)
	if err != nil {
		return nil, fmt.Errorf("encode capture stream terminal: %w", err)
	}
	if len(payload) > MaxTerminalPayloadBytes {
		return nil, fmt.Errorf(
			"capture stream terminal is %d bytes; limit is %d",
			len(payload),
			MaxTerminalPayloadBytes,
		)
	}
	return payload, nil
}

func encodeJSONLine(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func streamIDString(identifier [16]byte) string {
	return hex.EncodeToString(identifier[:])
}

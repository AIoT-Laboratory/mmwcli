// Package capturemanifest defines the versioned metadata written beside one
// raw ADC capture. It does not parse radar configuration or ADC samples.
package capturemanifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/radar"
)

const (
	SchemaV1            = "mmwcli.capture_session.v1"
	RadarConfigFileName = "radar.cfg"
)

type v1Manifest struct {
	Schema      string        `json:"schema"`
	Hardware    v1Hardware    `json:"hardware"`
	ADC         v1ADC         `json:"adc"`
	RadarConfig v1RadarConfig `json:"radar_config"`
}

type v1Hardware struct {
	Vendor         string `json:"vendor"`
	Family         string `json:"family"`
	Model          string `json:"model"`
	Revision       string `json:"revision"`
	IdentitySource string `json:"identity_source"`
}

type v1ADC struct {
	Path      string `json:"path"`
	DataType  string `json:"dtype"`
	ByteOrder string `json:"byte_order"`
	LaneCount uint8  `json:"lane_count"`
	Layout    string `json:"layout"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type v1RadarConfig struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
}

// NewV1Finalizer snapshots config and returns a finalizer for one capture
// session. It validates wire-level inputs, not CFG semantics. Before creating
// output or accessing hardware, the caller must preflight the v1-supported
// subset and build its plan-bound raw capture contract from this exact byte
// snapshot.
func NewV1Finalizer(
	config []byte,
	contract radar.RawCaptureContract,
) (capturefile.SessionFinalizer, error) {
	if len(bytes.TrimSpace(config)) == 0 {
		return nil, errors.New("capture session radar configuration is empty")
	}
	if !contract.Valid() {
		return nil, errors.New("capture session raw capture contract is invalid")
	}
	configSnapshot := bytes.Clone(config)
	configSHA256 := sha256.Sum256(configSnapshot)
	return func(ctx context.Context, stage capturefile.SessionStage) error {
		adc := stage.ADC()
		if adc.SizeBytes <= 0 {
			return errors.New("capture session ADC artifact is empty")
		}
		if adc.SizeBytes%2 != 0 {
			return fmt.Errorf("capture session ADC size %d is not aligned to int16", adc.SizeBytes)
		}
		record := v1Manifest{
			Schema: SchemaV1,
			Hardware: v1Hardware{
				Vendor:         contract.Vendor(),
				Family:         contract.Family(),
				Model:          contract.Model(),
				Revision:       contract.Revision(),
				IdentitySource: contract.IdentitySource(),
			},
			ADC: v1ADC{
				Path:      capturefile.SessionADCFileName,
				DataType:  contract.DataType(),
				ByteOrder: contract.ByteOrder(),
				LaneCount: contract.LaneCount(),
				Layout:    contract.Layout(),
				SizeBytes: adc.SizeBytes,
				SHA256:    hex.EncodeToString(adc.SHA256[:]),
			},
			RadarConfig: v1RadarConfig{
				Path:   RadarConfigFileName,
				Format: contract.ConfigFormat(),
				SHA256: hex.EncodeToString(configSHA256[:]),
			},
		}
		manifest, err := encodeV1(record, contract)
		if err != nil {
			return err
		}
		if err := stage.WriteFileContext(ctx, RadarConfigFileName, configSnapshot); err != nil {
			return fmt.Errorf("stage capture session radar configuration: %w", err)
		}
		if err := stage.WriteFileContext(ctx, capturefile.SessionManifestFileName, manifest); err != nil {
			return fmt.Errorf("stage capture session manifest: %w", err)
		}
		return nil
	}, nil
}

func encodeV1(record v1Manifest, contract radar.RawCaptureContract) ([]byte, error) {
	if err := record.validate(contract); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode capture session manifest: %w", err)
	}
	return append(encoded, '\n'), nil
}

func (record v1Manifest) validate(contract radar.RawCaptureContract) error {
	if record.Schema != SchemaV1 {
		return fmt.Errorf("invalid capture session schema %q", record.Schema)
	}
	if !contract.Valid() {
		return errors.New("capture session raw capture contract is invalid")
	}
	if record.Hardware != (v1Hardware{
		Vendor:         contract.Vendor(),
		Family:         contract.Family(),
		Model:          contract.Model(),
		Revision:       contract.Revision(),
		IdentitySource: contract.IdentitySource(),
	}) {
		return errors.New("capture session manifest has invalid hardware fields")
	}
	if record.ADC.Path != capturefile.SessionADCFileName ||
		record.ADC.DataType != contract.DataType() ||
		record.ADC.ByteOrder != contract.ByteOrder() ||
		record.ADC.LaneCount != contract.LaneCount() ||
		record.ADC.Layout != contract.Layout() ||
		record.ADC.SizeBytes <= 0 ||
		record.RadarConfig.Path != RadarConfigFileName ||
		record.RadarConfig.Format != contract.ConfigFormat() {
		return errors.New("capture session manifest has invalid required fields")
	}
	if !validSHA256(record.ADC.SHA256) || !validSHA256(record.RadarConfig.SHA256) {
		return errors.New("capture session manifest has an invalid SHA-256 digest")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

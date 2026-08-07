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
)

const (
	SchemaV1                 = "mmwcli.capture_session.v1"
	RadarConfigFileName      = "radar.cfg"
	RadarConfigFormatXWR68xx = "ti_xwr68xx_legacy_cli"
	ADCDataTypeInt16         = "int16"
	ADCByteOrderLittleEndian = "little"
	ADCLayoutGroup2IThenQ    = ADCLayout("group2_i_then_q")
)

// ADCLayout is the explicit raw complex-sample ordering recorded in v1.
type ADCLayout string

type v1Manifest struct {
	Schema      string        `json:"schema"`
	ADC         v1ADC         `json:"adc"`
	RadarConfig v1RadarConfig `json:"radar_config"`
}

type v1ADC struct {
	Path      string `json:"path"`
	DataType  string `json:"dtype"`
	ByteOrder string `json:"byte_order"`
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
// xWR68xx subset and build its radar plan from this exact byte snapshot.
func NewV1Finalizer(config []byte, layout ADCLayout) (capturefile.SessionFinalizer, error) {
	if len(bytes.TrimSpace(config)) == 0 {
		return nil, errors.New("capture session radar configuration is empty")
	}
	if layout != ADCLayoutGroup2IThenQ {
		return nil, fmt.Errorf("unsupported capture session ADC layout %q", layout)
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
			ADC: v1ADC{
				Path:      capturefile.SessionADCFileName,
				DataType:  ADCDataTypeInt16,
				ByteOrder: ADCByteOrderLittleEndian,
				Layout:    string(layout),
				SizeBytes: adc.SizeBytes,
				SHA256:    hex.EncodeToString(adc.SHA256[:]),
			},
			RadarConfig: v1RadarConfig{
				Path:   RadarConfigFileName,
				Format: RadarConfigFormatXWR68xx,
				SHA256: hex.EncodeToString(configSHA256[:]),
			},
		}
		manifest, err := encodeV1(record)
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

func encodeV1(record v1Manifest) ([]byte, error) {
	if err := record.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode capture session manifest: %w", err)
	}
	return append(encoded, '\n'), nil
}

func (record v1Manifest) validate() error {
	if record.Schema != SchemaV1 {
		return fmt.Errorf("invalid capture session schema %q", record.Schema)
	}
	if record.ADC.Path != capturefile.SessionADCFileName ||
		record.ADC.DataType != ADCDataTypeInt16 ||
		record.ADC.ByteOrder != ADCByteOrderLittleEndian ||
		record.ADC.Layout != string(ADCLayoutGroup2IThenQ) ||
		record.ADC.SizeBytes <= 0 ||
		record.RadarConfig.Path != RadarConfigFileName ||
		record.RadarConfig.Format != RadarConfigFormatXWR68xx {
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

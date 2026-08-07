package capturemanifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"mmwcli/internal/capturefile"
)

func TestV1FinalizerPublishesSelfContainedSession(t *testing.T) {
	config := []byte("flushCfg\r\nframeCfg 0 1 1 1 100 1 0\r\n")
	finalizer, err := NewV1Finalizer(config, ADCLayoutGroup2IThenQ)
	if err != nil {
		t.Fatal(err)
	}
	// The finalizer owns an immutable snapshot rather than the caller's slice.
	config[0] = 'X'
	output := filepath.Join(t.TempDir(), "capture-session")
	session, err := capturefile.CreateSessionDirectory(output, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	adcData := []byte{1, 2, 3, 4, 5, 6}
	if _, err := session.WriteAt(adcData, 0); err != nil {
		t.Fatal(err)
	}
	if err := session.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantConfig := []byte("flushCfg\r\nframeCfg 0 1 1 1 100 1 0\r\n")
	gotConfig, err := os.ReadFile(filepath.Join(output, RadarConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotConfig) != string(wantConfig) {
		t.Fatalf("radar.cfg = %q, want exact snapshot %q", gotConfig, wantConfig)
	}
	manifestData, err := os.ReadFile(filepath.Join(output, capturefile.SessionManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	var record v1Manifest
	if err := json.Unmarshal(manifestData, &record); err != nil {
		t.Fatal(err)
	}
	adcSHA256 := sha256.Sum256(adcData)
	configSHA256 := sha256.Sum256(wantConfig)
	want := v1Manifest{
		Schema: SchemaV1,
		ADC: v1ADC{
			Path:      capturefile.SessionADCFileName,
			DataType:  ADCDataTypeInt16,
			ByteOrder: ADCByteOrderLittleEndian,
			Layout:    string(ADCLayoutGroup2IThenQ),
			SizeBytes: int64(len(adcData)),
			SHA256:    hex.EncodeToString(adcSHA256[:]),
		},
		RadarConfig: v1RadarConfig{
			Path:   RadarConfigFileName,
			Format: RadarConfigFormatXWR68xx,
			SHA256: hex.EncodeToString(configSHA256[:]),
		},
	}
	if record != want {
		t.Fatalf("capture.json = %+v, want %+v", record, want)
	}
	assertV1WireContract(t, manifestData, adcSHA256, configSHA256, len(adcData))
	if manifestData[len(manifestData)-1] != '\n' {
		t.Fatal("capture.json has no trailing newline")
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Name() != capturefile.SessionADCFileName ||
		entries[1].Name() != capturefile.SessionManifestFileName || entries[2].Name() != RadarConfigFileName {
		t.Fatalf("session entries = %v", entryNames(entries))
	}
	if _, err := os.Stat(output + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session stage remains after commit: %v", err)
	}
}

func assertV1WireContract(
	t *testing.T,
	manifestData []byte,
	adcSHA256, configSHA256 [sha256.Size]byte,
	adcSize int,
) {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(manifestData, &wire); err != nil {
		t.Fatal(err)
	}
	requireJSONKeys(t, wire, "schema", "adc", "radar_config")
	if wire["schema"] != "mmwcli.capture_session.v1" {
		t.Fatalf("wire schema = %v", wire["schema"])
	}
	adc, ok := wire["adc"].(map[string]any)
	if !ok {
		t.Fatalf("wire adc = %#v", wire["adc"])
	}
	requireJSONKeys(t, adc, "path", "dtype", "byte_order", "layout", "size_bytes", "sha256")
	if adc["path"] != "adc.bin" || adc["dtype"] != "int16" || adc["byte_order"] != "little" ||
		adc["layout"] != "group2_i_then_q" || adc["size_bytes"] != float64(adcSize) ||
		adc["sha256"] != hex.EncodeToString(adcSHA256[:]) {
		t.Fatalf("wire adc = %#v", adc)
	}
	config, ok := wire["radar_config"].(map[string]any)
	if !ok {
		t.Fatalf("wire radar_config = %#v", wire["radar_config"])
	}
	requireJSONKeys(t, config, "path", "format", "sha256")
	if config["path"] != "radar.cfg" || config["format"] != "ti_xwr68xx_legacy_cli" ||
		config["sha256"] != hex.EncodeToString(configSHA256[:]) {
		t.Fatalf("wire radar_config = %#v", config)
	}
}

func requireJSONKeys(t *testing.T, object map[string]any, keys ...string) {
	t.Helper()
	if len(object) != len(keys) {
		t.Fatalf("JSON keys = %v, want %v", mapKeys(object), keys)
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			t.Fatalf("JSON keys = %v, missing %q", mapKeys(object), key)
		}
	}
}

func mapKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestNewV1FinalizerRejectsInvalidContractInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		config []byte
		layout ADCLayout
	}{
		{name: "nil config", layout: ADCLayoutGroup2IThenQ},
		{name: "blank config", config: []byte(" \r\n\t"), layout: ADCLayoutGroup2IThenQ},
		{name: "missing layout", config: []byte("flushCfg\n")},
		{name: "unknown layout", config: []byte("flushCfg\n"), layout: ADCLayout("interleaved")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewV1Finalizer(test.config, test.layout); err == nil {
				t.Fatal("NewV1Finalizer accepted invalid contract inputs")
			}
		})
	}
}

func TestV1FinalizerRejectsNonInt16ADCSize(t *testing.T) {
	finalizer, err := NewV1Finalizer([]byte("flushCfg\n"), ADCLayoutGroup2IThenQ)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 3} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture-session")
			session, err := capturefile.CreateSessionDirectory(output, finalizer)
			if err != nil {
				t.Fatal(err)
			}
			if size > 0 {
				if _, err := session.WriteAt(make([]byte, size), 0); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.CommitContext(context.Background()); err == nil {
				t.Fatal("CommitContext published an invalid int16 ADC artifact")
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid session was published: %v", err)
			}
			info, err := os.Stat(filepath.Join(output+".part", capturefile.SessionADCFileName))
			if err != nil {
				t.Fatalf("ADC failure evidence missing: %v", err)
			}
			if info.Size() != int64(size) {
				t.Fatalf("retained ADC size = %d, want %d", info.Size(), size)
			}
		})
	}
}

func TestV1ManifestValidationRejectsInvalidDigests(t *testing.T) {
	record := v1Manifest{
		Schema: SchemaV1,
		ADC: v1ADC{
			Path:      capturefile.SessionADCFileName,
			DataType:  ADCDataTypeInt16,
			ByteOrder: ADCByteOrderLittleEndian,
			Layout:    string(ADCLayoutGroup2IThenQ),
			SizeBytes: 1,
			SHA256:    "ABC",
		},
		RadarConfig: v1RadarConfig{
			Path:   RadarConfigFileName,
			Format: RadarConfigFormatXWR68xx,
			SHA256: "0",
		},
	}
	if _, err := encodeV1(record); err == nil {
		t.Fatal("encodeV1 accepted invalid SHA-256 digests")
	}
}

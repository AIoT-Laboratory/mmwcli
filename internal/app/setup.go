package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"

	"mmwcli/internal/camera"
)

const (
	setupSchema          = "mmwcli.setup.v1"
	defaultMountPitchDeg = 0
	levelROIFrame        = "level_forward_lateral_up"
)

type setupConfig struct {
	Schema string             `json:"schema"`
	Radar  setupRadar         `json:"radar"`
	DCA    setupDCA           `json:"dca"`
	Mount  setupMount         `json:"mount"`
	ROI    *setupROI          `json:"roi,omitempty"`
	Camera *setupCameraFormat `json:"camera,omitempty"`

	path    string
	bssPath string
	mssPath string
}

type setupRadar struct {
	Port string `json:"port"`
	BSS  string `json:"bss"`
	MSS  string `json:"mss"`
	D2XX string `json:"d2xx"`
}

type setupDCA struct {
	Host    string `json:"host"`
	Device  string `json:"device"`
	DelayUS int    `json:"delay_us"`
}

type setupMount struct {
	HeightM  float64 `json:"height_m"`
	PitchDeg float64 `json:"pitch_deg"`
}

type setupROI struct {
	Frame string      `json:"frame"`
	MinM  setupVector `json:"min_m"`
	MaxM  setupVector `json:"max_m"`
}

type setupVector [3]float64

func (vector *setupVector) UnmarshalJSON(encoded []byte) error {
	var values []float64
	if err := json.Unmarshal(encoded, &values); err != nil {
		return err
	}
	if len(values) != len(vector) {
		return errors.New("setup ROI vectors must contain exactly three values")
	}
	copy(vector[:], values)
	return nil
}

type setupCameraFormat struct {
	Width    int `json:"width"`
	Height   int `json:"height"`
	FPS      int `json:"fps"`
	MaxBytes int `json:"max_bytes"`
}

func loadSetup(path string) (setupConfig, error) {
	if strings.TrimSpace(path) == "" {
		return setupConfig{}, usageError{message: "--setup is required"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return setupConfig{}, fmt.Errorf("resolve setup: %w", err)
	}
	encoded, err := os.ReadFile(abs)
	if err != nil {
		return setupConfig{}, fmt.Errorf("read setup %s: %w", abs, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	setup := setupConfig{Mount: setupMount{PitchDeg: defaultMountPitchDeg}}
	if err := decoder.Decode(&setup); err != nil {
		return setupConfig{}, fmt.Errorf("decode setup %s: %w", abs, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return setupConfig{}, errors.New("setup must contain exactly one JSON object")
	}
	if err := setup.validate(); err != nil {
		return setupConfig{}, err
	}
	base := filepath.Dir(abs)
	setup.path = abs
	setup.bssPath = resolveSetupPath(base, setup.Radar.BSS)
	setup.mssPath = resolveSetupPath(base, setup.Radar.MSS)
	setup.DCA.Host = net.ParseIP(setup.DCA.Host).To4().String()
	setup.DCA.Device = net.ParseIP(setup.DCA.Device).To4().String()
	return setup, nil
}

func resolveSetupPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func (setup setupConfig) validate() error {
	if setup.Schema != setupSchema {
		return fmt.Errorf("setup schema is %q, want %q", setup.Schema, setupSchema)
	}
	for name, value := range map[string]string{
		"radar.port": setup.Radar.Port,
		"radar.bss":  setup.Radar.BSS,
		"radar.mss":  setup.Radar.MSS,
		"radar.d2xx": setup.Radar.D2XX,
	} {
		if value == "" || value != strings.TrimSpace(value) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("setup %s must be a non-empty exact value", name)
		}
	}
	if net.ParseIP(setup.DCA.Host).To4() == nil || net.ParseIP(setup.DCA.Device).To4() == nil {
		return errors.New("setup dca.host and dca.device must be IPv4 addresses")
	}
	if setup.DCA.DelayUS < 5 || setup.DCA.DelayUS > 500 {
		return errors.New("setup dca.delay_us must be in 5..500")
	}
	if err := validateMount(setup.Mount); err != nil {
		return err
	}
	if setup.ROI != nil {
		if err := validateROI(*setup.ROI); err != nil {
			return err
		}
	}
	if setup.Camera != nil {
		if err := setup.Camera.config("validation-device").Validate(); err != nil {
			return fmt.Errorf("setup camera: %w", err)
		}
	}
	return nil
}

func validateMount(mount setupMount) error {
	if math.IsNaN(mount.HeightM) || math.IsInf(mount.HeightM, 0) || mount.HeightM <= 0 || mount.HeightM > 10 {
		return errors.New("setup mount.height_m must be in (0, 10]")
	}
	if math.IsNaN(mount.PitchDeg) || math.IsInf(mount.PitchDeg, 0) ||
		(mount.PitchDeg != 0 && mount.PitchDeg != 30 && mount.PitchDeg != 90) {
		return errors.New("setup mount.pitch_deg must be 0, 30, or 90")
	}
	return nil
}

func validateROI(roi setupROI) error {
	if roi.Frame != levelROIFrame {
		return fmt.Errorf("setup roi.frame must be %q", levelROIFrame)
	}
	for index, axis := range []string{"forward", "lateral", "up"} {
		minimum, maximum := roi.MinM[index], roi.MaxM[index]
		if math.IsNaN(minimum) || math.IsInf(minimum, 0) ||
			math.IsNaN(maximum) || math.IsInf(maximum, 0) || minimum >= maximum {
			return fmt.Errorf("setup roi %s bounds must be finite and increasing", axis)
		}
	}
	if roi.MinM[0] < 0 {
		return errors.New("setup roi minimum forward distance must be non-negative")
	}
	if roi.MinM[2] < 0 {
		return errors.New("setup roi minimum height must be non-negative")
	}
	return nil
}

func (format setupCameraFormat) config(device string) camera.Config {
	return camera.Config{
		Device: device, Width: format.Width, Height: format.Height,
		FPS: format.FPS, MaxBytes: format.MaxBytes,
	}
}

func (setup setupConfig) cameraConfig(device string, radarOnly bool) (*camera.Config, error) {
	if radarOnly {
		return nil, nil
	}
	if strings.TrimSpace(device) == "" {
		return nil, usageError{message: "--camera is required unless --radar-only is set"}
	}
	if setup.Camera == nil {
		return nil, errors.New("setup camera format is required for camera preview or capture")
	}
	configured := setup.Camera.config(device)
	if err := configured.Validate(); err != nil {
		return nil, fmt.Errorf("camera selection is invalid: %w", err)
	}
	return &configured, nil
}

func writeSetup(path string, setup setupConfig) error {
	encoded, err := json.MarshalIndent(setup, "", "  ")
	if err != nil {
		return fmt.Errorf("encode setup: %w", err)
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open setup for update: %w", err)
	}
	written, writeErr := file.Write(encoded)
	if writeErr == nil && written != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}

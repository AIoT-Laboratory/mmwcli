package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"mmwcli/internal/camera"
)

const rigSchema = "mmwcli.rig.v2"

type rigConfig struct {
	Schema  string         `json:"schema"`
	Port    string         `json:"port"`
	BSS     string         `json:"bss"`
	MSS     string         `json:"mss"`
	D2XX    string         `json:"d2xx"`
	DCA     rigDCA         `json:"dca"`
	Camera  *camera.Config `json:"camera,omitempty"`
	HeightM float64        `json:"height_m"`
}

type rigDCA struct {
	Host    string `json:"host"`
	Device  string `json:"device"`
	DelayUS int    `json:"delay_us"`
}

func loadRig(path string, radarOnly bool, cameraDevice string) (rigConfig, error) {
	if strings.TrimSpace(path) == "" {
		return rigConfig{}, usageError{message: "--rig is required"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return rigConfig{}, fmt.Errorf("resolve rig: %w", err)
	}
	encoded, err := os.ReadFile(abs)
	if err != nil {
		return rigConfig{}, fmt.Errorf("read rig %s: %w", abs, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var rig rigConfig
	if err := decoder.Decode(&rig); err != nil {
		return rigConfig{}, fmt.Errorf("decode rig %s: %w", abs, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return rigConfig{}, errors.New("rig must contain exactly one JSON object")
	}
	base := filepath.Dir(abs)
	rig.BSS = resolveRigPath(base, rig.BSS)
	rig.MSS = resolveRigPath(base, rig.MSS)
	if cameraDevice != "" {
		if rig.Camera == nil {
			return rigConfig{}, errors.New("rig camera is required for --camera")
		}
		configured := *rig.Camera
		configured.Device = cameraDevice
		rig.Camera = &configured
	}
	if err := rig.validate(radarOnly); err != nil {
		return rigConfig{}, err
	}
	return rig, nil
}

func resolveRigPath(base, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(base, value))
}

func (rig rigConfig) validate(radarOnly bool) error {
	if rig.Schema != rigSchema {
		return fmt.Errorf("rig schema is %q, want %q", rig.Schema, rigSchema)
	}
	for name, value := range map[string]string{
		"port": rig.Port, "bss": rig.BSS, "mss": rig.MSS,
		"d2xx": rig.D2XX,
	} {
		if value == "" || value != strings.TrimSpace(value) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("rig %s must be a non-empty exact value", name)
		}
	}
	if net.ParseIP(rig.DCA.Host).To4() == nil || net.ParseIP(rig.DCA.Device).To4() == nil {
		return errors.New("rig dca.host and dca.device must be IPv4 addresses")
	}
	if rig.DCA.DelayUS < 5 || rig.DCA.DelayUS > 500 {
		return errors.New("rig dca.delay_us must be in 5..500")
	}
	if rig.HeightM <= 0 || rig.HeightM > 10 {
		return errors.New("rig height_m must be in (0, 10]")
	}
	if radarOnly {
		return nil
	}
	if rig.Camera == nil {
		return errors.New("rig camera is required unless --radar-only is set")
	}
	if err := rig.Camera.Validate(); err != nil {
		return fmt.Errorf("rig camera: %w", err)
	}
	return nil
}

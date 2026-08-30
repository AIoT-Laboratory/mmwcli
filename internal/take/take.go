// Package take publishes one flat, finite IWR6843 capture transaction.
package take

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mmwcli/internal/camera"
	"mmwcli/internal/capturefile"
	"mmwcli/internal/radar"
)

const (
	Schema          = "mmwcli.take.v3"
	ManifestName    = "session.json"
	SetupName       = "setup.json"
	SetupSchema     = "mmwcli.snapshot.v1"
	RadarPayload    = "adc.bin"
	RadarConfigName = "radar.cfg"
)

type Config struct {
	Output      string
	RadarConfig []byte
	Plan        radar.Plan
	Setup       SetupSnapshot
	Camera      *camera.Config
}

type SetupSnapshot struct {
	Schema string         `json:"schema"`
	Radar  SetupRadar     `json:"radar"`
	DCA    SetupDCA       `json:"dca"`
	Mount  SetupMount     `json:"mount"`
	Camera *camera.Config `json:"camera"`
}

type SetupRadar struct {
	Model    string    `json:"model"`
	Revision string    `json:"revision"`
	Port     string    `json:"port"`
	BSS      SetupFile `json:"bss"`
	MSS      SetupFile `json:"mss"`
	D2XX     string    `json:"d2xx"`
}

type SetupFile struct {
	Name   string `json:"name"`
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type SetupDCA struct {
	Host    string `json:"host"`
	Device  string `json:"device"`
	DelayUS int    `json:"delay_us"`
}

type SetupMount struct {
	HeightM  float64 `json:"height_m"`
	PitchDeg float64 `json:"pitch_deg"`
}

type Capture struct {
	directory *capturefile.TransactionDirectory
	radar     *capturefile.File
	camera    *camera.Recorder
	config    Config
	id        string
	origin    time.Time

	mu                  sync.Mutex
	frameStartLowerNS   uint64
	frameStartUpperNS   uint64
	frameStartObserved  bool
	participantFinished bool
	published           bool
}

type artifact struct {
	Path   string `json:"path"`
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type frameStart struct {
	LowerNS uint64 `json:"lower_ns"`
	UpperNS uint64 `json:"upper_ns"`
}

type radarRecord struct {
	Model    string   `json:"model"`
	Revision string   `json:"revision"`
	ADC      artifact `json:"adc"`
	Config   artifact `json:"config"`
}

type cameraRecord struct {
	Frames        uint64   `json:"frames"`
	TimeSemantics string   `json:"time_semantics"`
	TickHz        uint64   `json:"tick_hz"`
	Payload       artifact `json:"payload"`
	Index         artifact `json:"index"`
}

type manifest struct {
	Schema        string        `json:"schema"`
	SessionID     string        `json:"session_id"`
	FrameCount    uint16        `json:"frame_count"`
	FramePeriodNS uint64        `json:"frame_period_ns"`
	RadarStart    frameStart    `json:"radar_start"`
	Setup         artifact      `json:"setup"`
	Radar         radarRecord   `json:"radar"`
	Camera        *cameraRecord `json:"camera,omitempty"`
}

func New(
	ctx context.Context,
	cancel context.CancelFunc,
	config Config,
	stderr io.Writer,
) (*Capture, error) {
	if ctx == nil || cancel == nil || !strings.HasSuffix(strings.ToLower(filepath.Clean(config.Output)), ".capture") || len(config.RadarConfig) == 0 ||
		config.Plan.NumberOfFrames == 0 || config.Plan.ExpectedBytes <= 0 {
		return nil, errors.New("take configuration is incomplete")
	}
	if err := validateSetupSnapshot(config.Setup, config.Camera); err != nil {
		return nil, fmt.Errorf("take setup snapshot is invalid: %w", err)
	}
	directory, err := capturefile.CreateTransactionDirectory(config.Output)
	if err != nil {
		return nil, err
	}
	setupBytes, err := json.MarshalIndent(config.Setup, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode setup snapshot: %w", err)
	}
	setupBytes = append(setupBytes, '\n')
	if err := writeFile(ctx, filepath.Join(directory.PartPath(), SetupName), setupBytes); err != nil {
		return nil, err
	}
	radarOutput, err := capturefile.Create(filepath.Join(directory.PartPath(), RadarPayload))
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		_ = radarOutput.Close()
		return nil, err
	}
	capture := &Capture{
		directory: directory, radar: radarOutput, config: config, id: id, origin: time.Now(),
	}
	if config.Camera != nil {
		recorder, err := camera.New(ctx, cancel, *config.Camera, capture.origin, directory.PartPath(), stderr)
		if err != nil {
			_ = radarOutput.Close()
			return nil, err
		}
		capture.camera = recorder
	}
	return capture, nil
}

func (capture *Capture) RadarOutput() capturefile.Output { return capture.radar }

func (capture *Capture) Arm(ctx context.Context) error {
	if capture.camera == nil {
		return nil
	}
	return capture.camera.Arm(ctx)
}

func (capture *Capture) Start(ctx context.Context) error {
	if capture.camera == nil {
		return nil
	}
	return capture.camera.Start(ctx)
}

func (capture *Capture) Finish(ctx context.Context, complete bool) error {
	capture.mu.Lock()
	capture.participantFinished = true
	capture.mu.Unlock()
	if capture.camera == nil {
		return nil
	}
	return capture.camera.Finish(ctx, complete)
}

func (capture *Capture) SetRadarStart(lower, upper time.Time) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.frameStartObserved || lower.Before(capture.origin) || upper.Before(lower) {
		return
	}
	capture.frameStartLowerNS = uint64(lower.Sub(capture.origin))
	capture.frameStartUpperNS = uint64(upper.Sub(capture.origin))
	capture.frameStartObserved = true
}

func (capture *Capture) Publish(ctx context.Context) error {
	capture.mu.Lock()
	if capture.published {
		capture.mu.Unlock()
		return errors.New("take is already published")
	}
	if !capture.participantFinished || !capture.frameStartObserved {
		capture.mu.Unlock()
		return errors.New("take lifecycle or radar start observation is incomplete")
	}
	start := frameStart{LowerNS: capture.frameStartLowerNS, UpperNS: capture.frameStartUpperNS}
	capture.mu.Unlock()
	if !capture.radar.Committed() {
		return errors.New("radar capture is not committed")
	}

	radarRecord, err := capture.radarRecord(ctx)
	if err != nil {
		return err
	}
	var recordedCamera *cameraRecord
	if capture.camera != nil {
		result, err := capture.camera.Result()
		if err != nil {
			return err
		}
		recordedCamera = &cameraRecord{
			Frames: result.FrameCount, TimeSemantics: "delivery_observed", TickHz: 1_000_000_000,
			Payload: cameraArtifact(result.Payload), Index: cameraArtifact(result.Index),
		}
	}
	record := manifest{
		Schema: Schema, SessionID: capture.id,
		FrameCount:    capture.config.Plan.NumberOfFrames,
		FramePeriodNS: uint64(capture.config.Plan.FramePeriod),
		RadarStart:    start,
		Radar:         radarRecord, Camera: recordedCamera,
	}
	record.Setup, err = fileArtifact(ctx, filepath.Join(capture.directory.PartPath(), SetupName), SetupName)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode take manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeFile(ctx, filepath.Join(capture.directory.PartPath(), ManifestName), encoded); err != nil {
		return err
	}
	if err := validateFiles(capture.directory.PartPath(), capture.camera != nil); err != nil {
		return err
	}
	if err := capture.directory.CommitContext(ctx); err != nil {
		return err
	}
	capture.mu.Lock()
	capture.published = true
	capture.mu.Unlock()
	return nil
}

func (capture *Capture) radarRecord(ctx context.Context) (radarRecord, error) {
	root := capture.directory.PartPath()
	adc, err := fileArtifact(ctx, filepath.Join(root, RadarPayload), RadarPayload)
	if err != nil {
		return radarRecord{}, err
	}
	if uint64(capture.config.Plan.ExpectedBytes) != adc.Bytes {
		return radarRecord{}, errors.New("radar ADC size does not match the capture plan")
	}
	if err := writeFile(ctx, filepath.Join(root, RadarConfigName), capture.config.RadarConfig); err != nil {
		return radarRecord{}, err
	}
	config, err := fileArtifact(ctx, filepath.Join(root, RadarConfigName), RadarConfigName)
	if err != nil {
		return radarRecord{}, err
	}
	return radarRecord{Model: "iwr6843", Revision: "es2", ADC: adc, Config: config}, nil
}

func (capture *Capture) Close() error {
	if capture == nil {
		return nil
	}
	capture.mu.Lock()
	published := capture.published
	participantFinished := capture.participantFinished
	capture.mu.Unlock()
	if published {
		return nil
	}
	var cameraErr error
	if capture.camera != nil && !participantFinished {
		cameraErr = capture.camera.Finish(context.Background(), false)
	}
	return errors.Join(cameraErr, capture.radar.Close())
}

func cameraArtifact(value camera.Artifact) artifact {
	return artifact{Path: value.Path, Bytes: value.Bytes, SHA256: hex.EncodeToString(value.SHA256[:])}
}

func fileArtifact(ctx context.Context, source, targetName string) (artifact, error) {
	file, err := os.Open(source)
	if err != nil {
		return artifact{}, err
	}
	digest := sha256.New()
	buffer := make([]byte, 1<<20)
	var size uint64
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return artifact{}, err
		}
		read, readErr := file.Read(buffer)
		if read != 0 {
			_, _ = digest.Write(buffer[:read])
			size += uint64(read)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return artifact{}, readErr
		}
	}
	if err := file.Close(); err != nil {
		return artifact{}, err
	}
	return artifact{Path: targetName, Bytes: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func writeFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create staged take file: %w", err)
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return nil
}

func validateFiles(root string, hasCamera bool) error {
	wanted := map[string]bool{
		ManifestName: true, SetupName: true, RadarPayload: true, RadarConfigName: true,
	}
	if hasCamera {
		wanted[camera.PayloadName] = true
		wanted[camera.IndexName] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != len(wanted) {
		return fmt.Errorf("take has %d files, want %d", len(entries), len(wanted))
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] || entry.IsDir() {
			return fmt.Errorf("take contains unexpected entry %q", entry.Name())
		}
	}
	return nil
}

func validateSetupSnapshot(snapshot SetupSnapshot, selectedCamera *camera.Config) error {
	if snapshot.Schema != SetupSchema || snapshot.Radar.Model != "iwr6843" ||
		snapshot.Radar.Revision != "es2" || !exactValue(snapshot.Radar.Port) ||
		!exactValue(snapshot.Radar.D2XX) || !canonicalIPv4(snapshot.DCA.Host) || !canonicalIPv4(snapshot.DCA.Device) ||
		snapshot.DCA.DelayUS < 5 || snapshot.DCA.DelayUS > 500 ||
		math.IsNaN(snapshot.Mount.HeightM) || math.IsInf(snapshot.Mount.HeightM, 0) ||
		snapshot.Mount.HeightM <= 0 || snapshot.Mount.HeightM > 10 || !validMountPitch(snapshot.Mount.PitchDeg) {
		return errors.New("setup snapshot is incomplete")
	}
	for _, file := range []SetupFile{snapshot.Radar.BSS, snapshot.Radar.MSS} {
		if !exactValue(file.Name) || filepath.Base(file.Name) != file.Name || file.Bytes == 0 ||
			len(file.SHA256) != sha256.Size*2 || file.SHA256 != strings.ToLower(file.SHA256) {
			return errors.New("setup snapshot firmware identity is incomplete")
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return errors.New("setup snapshot firmware SHA-256 is invalid")
		}
	}
	if (snapshot.Camera == nil) != (selectedCamera == nil) {
		return errors.New("setup snapshot camera does not match capture")
	}
	if snapshot.Camera != nil {
		if err := snapshot.Camera.Validate(); err != nil || *snapshot.Camera != *selectedCamera {
			return errors.New("setup snapshot camera does not match capture")
		}
	}
	return nil
}

func validMountPitch(pitchDeg float64) bool {
	return pitchDeg == 0 || pitchDeg == 90
}

func exactValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsRune(value, 0)
}

func canonicalIPv4(value string) bool {
	parsed := net.ParseIP(value)
	return parsed != nil && parsed.To4() != nil && parsed.To4().String() == value
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create capture id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

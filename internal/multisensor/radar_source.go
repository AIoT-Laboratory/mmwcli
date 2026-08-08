package multisensor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturemanifest"
	"mmwcli/internal/radar"
)

// FrameStartBracket is session-relative host-monotonic evidence enclosing the
// first radar frame start. It does not claim a device clock observation.
type FrameStartBracket struct {
	LowerNS uint64
	UpperNS uint64
}

// FinalizeRadarSource adds the deterministic frame index to one already
// published capture-session directory and returns its complete aggregate
// source declaration. The directory must be an aggregate staging child named
// exactly sourceID; this function does not move or publish the outer aggregate.
func FinalizeRadarSource(
	ctx context.Context,
	directoryPath string,
	configSnapshot []byte,
	plan radar.CapturePlan,
	producerVersion string,
	frameStart FrameStartBracket,
	sourceID string,
) (Source, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	if frameStart.LowerNS > frameStart.UpperNS {
		return Source{}, errors.New("radar frame-start bracket lower bound exceeds upper bound")
	}
	if err := validateOpaqueID("producer.version", producerVersion); err != nil {
		return Source{}, err
	}
	if len(sourceID) > MaximumSourceIDBytes || !sourceIDPattern.MatchString(sourceID) {
		return Source{}, fmt.Errorf("source_id %q is not a safe lowercase directory leaf", sourceID)
	}
	if directoryPath == "" || strings.HasSuffix(strings.ToLower(filepath.Base(directoryPath)), ".part") {
		return Source{}, errors.New("radar source path is not a committed capture-session directory")
	}
	if filepath.Base(filepath.Clean(directoryPath)) != sourceID {
		return Source{}, fmt.Errorf("radar source directory leaf must equal source_id %q", sourceID)
	}
	if err := radar.ValidateCaptureSessionV1Plan(configSnapshot, plan); err != nil {
		return Source{}, fmt.Errorf("validate radar source plan: %w", err)
	}
	frameCount, frameBytes, payloadBytes, periodNS, endTick, err := radarSourceGeometry(plan)
	if err != nil {
		return Source{}, err
	}

	initial, err := inspectRadarSourceFiles(directoryPath, []string{
		capturefile.SessionADCFileName,
		capturemanifest.RadarConfigFileName,
		capturefile.SessionManifestFileName,
	})
	if err != nil {
		return Source{}, err
	}
	if initial[capturefile.SessionADCFileName] != payloadBytes {
		return Source{}, fmt.Errorf(
			"radar ADC size is %d bytes; finite plan requires exactly %d",
			initial[capturefile.SessionADCFileName],
			payloadBytes,
		)
	}
	if initial[capturemanifest.RadarConfigFileName] != uint64(len(configSnapshot)) {
		return Source{}, errors.New("published radar.cfg size differs from the exact configuration snapshot")
	}
	publishedConfig, err := os.ReadFile(filepath.Join(directoryPath, capturemanifest.RadarConfigFileName))
	if err != nil {
		return Source{}, fmt.Errorf("read published radar.cfg: %w", err)
	}
	if !bytes.Equal(publishedConfig, configSnapshot) {
		return Source{}, errors.New("published radar.cfg bytes differ from the exact configuration snapshot")
	}

	limits := SourceLimits{
		MaxItems:        frameCount,
		MaxItemBytes:    frameBytes,
		MaxPayloadBytes: payloadBytes,
	}
	index := SensorIndex{PayloadBytes: payloadBytes, Entries: make([]IndexEntry, int(frameCount))}
	for item := uint64(0); item < frameCount; item++ {
		offset, _ := checkedMulU64(item, frameBytes)
		tick, _ := checkedMulU64(item, periodNS)
		index.Entries[item] = IndexEntry{
			ItemIndex: item, PayloadOffset: offset, PayloadSize: frameBytes,
			Tick: tick, DurationTicks: periodNS, SyncEventID: NoSyncEventID,
		}
	}
	encodedIndex, err := EncodeSensorIndex(index, limits)
	if err != nil {
		return Source{}, fmt.Errorf("encode radar frame index: %w", err)
	}

	artifacts := make([]Artifact, 0, 4)
	for _, item := range []struct {
		role ArtifactRole
		name string
	}{
		{role: ArtifactPayload, name: capturefile.SessionADCFileName},
		{role: ArtifactConfiguration, name: capturemanifest.RadarConfigFileName},
		{role: ArtifactManifest, name: capturefile.SessionManifestFileName},
	} {
		digest, err := hashExactFile(ctx, filepath.Join(directoryPath, item.name), initial[item.name])
		if err != nil {
			return Source{}, fmt.Errorf("hash radar source artifact %q: %w", item.name, err)
		}
		artifacts = append(artifacts, Artifact{
			Role: item.role, Path: item.name, SizeBytes: initial[item.name],
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	indexDigest := sha256.Sum256(encodedIndex)
	artifacts = append(artifacts, Artifact{
		Role: ArtifactIndex, Path: IndexFileName, SizeBytes: uint64(len(encodedIndex)),
		SHA256: hex.EncodeToString(indexDigest[:]),
	})

	width := frameStart.UpperNS - frameStart.LowerNS
	uncertainty := width/2 + width%2
	midpoint := frameStart.LowerNS + width/2
	observationID := sourceID + "-frame-start"
	source := Source{
		SourceID: sourceID, Kind: SourceRadar, Required: true, Outcome: OutcomeComplete,
		Producer: Producer{Name: "mmwcli", Version: producerVersion},
		Limits:   limits,
		Payload: PayloadContract{
			Filename: capturefile.SessionADCFileName,
			Format:   plan.RawCapture.ConfigFormat(),
		},
		ItemCount: frameCount, PayloadBytes: payloadBytes,
		Clock: Clock{
			ClockID: sourceID + "-frame-clock", TickHz: 1_000_000_000,
			TimestampSemantics: TimestampFrameStart,
		},
		ClockObservations: []ClockObservation{{
			ObservationID: observationID, Tick: 0,
			HostBeforeNS: frameStart.LowerNS, HostAfterNS: frameStart.UpperNS,
		}},
		AffineSegments: []AffineSegment{{
			StartUnwrappedTick: 0, EndUnwrappedTick: endTick,
			SourceOriginTick: 0, HostOriginNS: midpoint,
			ScaleNum: 1, ScaleDen: 1,
			ObservationIDs: []string{observationID}, UncertaintyNS: uncertainty,
		}},
		Artifacts: artifacts, ApplicationMetadata: ApplicationMetadata{},
	}
	if err := validateSource(source); err != nil {
		return Source{}, fmt.Errorf("validate radar aggregate source: %w", err)
	}
	if err := validateSourceIndex(source, index, map[uint64]SyncEvent{}); err != nil {
		return Source{}, fmt.Errorf("validate radar aggregate index: %w", err)
	}
	if err := writeRadarIndex(ctx, filepath.Join(directoryPath, IndexFileName), encodedIndex); err != nil {
		return Source{}, err
	}
	final, err := inspectRadarSourceFiles(directoryPath, []string{
		capturefile.SessionADCFileName,
		capturemanifest.RadarConfigFileName,
		capturefile.SessionManifestFileName,
		IndexFileName,
	})
	if err != nil {
		return Source{}, err
	}
	for _, artifact := range source.Artifacts {
		if final[artifact.Path] != artifact.SizeBytes {
			return Source{}, fmt.Errorf("radar source artifact %q changed size during finalization", artifact.Path)
		}
		digest, err := hashExactFile(ctx, filepath.Join(directoryPath, artifact.Path), artifact.SizeBytes)
		if err != nil {
			return Source{}, err
		}
		if hex.EncodeToString(digest[:]) != artifact.SHA256 {
			return Source{}, fmt.Errorf("radar source artifact %q changed during finalization", artifact.Path)
		}
	}
	return source, nil
}

func radarSourceGeometry(plan radar.CapturePlan) (
	frameCount uint64,
	frameBytes uint64,
	payloadBytes uint64,
	periodNS uint64,
	endTick uint64,
	err error,
) {
	if plan.InfiniteFrames || plan.NumberOfFrames == 0 || plan.BytesPerFrame <= 0 ||
		plan.ExpectedBytes <= 0 || plan.FramePeriod <= 0 || !plan.RawCapture.Valid() {
		return 0, 0, 0, 0, 0, errors.New("radar aggregate source requires a valid finite capture plan")
	}
	frameCount = uint64(plan.NumberOfFrames)
	frameBytes = uint64(plan.BytesPerFrame)
	payloadBytes = uint64(plan.ExpectedBytes)
	periodNS = uint64(plan.FramePeriod)
	computedPayload, ok := checkedMulU64(frameCount, frameBytes)
	if !ok || computedPayload != payloadBytes {
		return 0, 0, 0, 0, 0, errors.New("radar finite plan frame geometry does not equal ExpectedBytes")
	}
	endTick, ok = checkedMulU64(frameCount, periodNS)
	if !ok || endTick == 0 {
		return 0, 0, 0, 0, 0, errors.New("radar finite plan frame clock range overflows")
	}
	return frameCount, frameBytes, payloadBytes, periodNS, endTick, nil
}

func inspectRadarSourceFiles(path string, expected []string) (map[string]uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect radar source directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("radar source path is not a committed regular directory")
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		wanted[name] = struct{}{}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read radar source directory: %w", err)
	}
	if len(entries) != len(wanted) {
		return nil, fmt.Errorf("radar source directory has %d files; expected exactly %d", len(entries), len(wanted))
	}
	sizes := make(map[string]uint64, len(entries))
	for _, entry := range entries {
		if _, ok := wanted[entry.Name()]; !ok {
			return nil, fmt.Errorf("radar source directory contains undeclared file %q", entry.Name())
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect radar source artifact %q: %w", entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 || entryInfo.Size() < 0 {
			return nil, fmt.Errorf("radar source artifact %q is not a regular file", entry.Name())
		}
		sizes[entry.Name()] = uint64(entryInfo.Size())
	}
	return sizes, nil
}

func writeRadarIndex(ctx context.Context, path string, encoded []byte) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create radar index.bin: %w", err)
	}
	remove := true
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close radar index.bin: %w", closeErr))
			}
		}
		if remove {
			resultErr = errors.Join(resultErr, os.Remove(path))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	written, err := io.Copy(file, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("write radar index.bin: %w", err)
	}
	if written != int64(len(encoded)) {
		return fmt.Errorf("write radar index.bin: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync radar index.bin: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close radar index.bin: %w", err)
	}
	closed = true
	remove = false
	return nil
}

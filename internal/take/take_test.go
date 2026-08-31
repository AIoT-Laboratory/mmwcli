package take

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mmwcli/internal/radar"
)

func TestRadarOnlyTakePublishesFlatFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := filepath.Join(t.TempDir(), "take.capture")
	capture, err := New(ctx, cancel, Config{
		Output:      output,
		RadarConfig: []byte("frameCfg fixture\n"),
		Plan: radar.Plan{
			NumberOfFrames: 1,
			FramePeriod:    time.Millisecond,
			BytesPerFrame:  4,
			ExpectedBytes:  4,
		},
		Setup: testSetupSnapshot(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	if got, want := capture.directory.PartPath(), output+".part"; got != want {
		t.Fatalf("capture stage = %q, want %q", got, want)
	}
	setupBytes, err := os.ReadFile(filepath.Join(output+".part", SetupName))
	if err != nil || !json.Valid(setupBytes) {
		t.Fatalf("frozen setup snapshot = %q, %v", setupBytes, err)
	}
	if _, err := capture.RadarOutput().WriteAt([]byte{1, 2, 3, 4}, 0); err != nil {
		t.Fatal(err)
	}
	if err := capture.RadarOutput().Truncate(4); err != nil {
		t.Fatal(err)
	}
	if err := capture.RadarOutput().CommitContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := capture.Finish(ctx, true); err != nil {
		t.Fatal(err)
	}
	now := capture.origin.Add(time.Millisecond)
	capture.SetRadarStart(now, now.Add(time.Microsecond))
	if err := capture.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("take file count = %d", len(entries))
	}
	manifestBytes, err := os.ReadFile(filepath.Join(output, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var record manifest
	if err := json.Unmarshal(manifestBytes, &record); err != nil {
		t.Fatal(err)
	}
	if record.Schema != Schema || record.FrameCount != 1 || record.Setup.Path != SetupName || record.Camera != nil {
		t.Fatalf("unexpected manifest: %+v", record)
	}
	wantHash := sha256.Sum256(setupBytes)
	if record.Setup.Bytes != uint64(len(setupBytes)) || record.Setup.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("setup reference = %+v", record.Setup)
	}
}

func TestSetupSnapshotAcceptsOnlySupportedMountPitches(t *testing.T) {
	for _, pitchDeg := range []float64{0, 90} {
		snapshot := testSetupSnapshot()
		snapshot.Mount.PitchDeg = pitchDeg
		if err := validateSetupSnapshot(snapshot, nil); err != nil {
			t.Fatalf("pitch %v rejected: %v", pitchDeg, err)
		}
	}

	for _, pitchDeg := range []float64{-90, 45, math.Inf(1), math.NaN()} {
		snapshot := testSetupSnapshot()
		snapshot.Mount.PitchDeg = pitchDeg
		if err := validateSetupSnapshot(snapshot, nil); err == nil {
			t.Fatalf("pitch %v accepted", pitchDeg)
		}
	}
}

func TestSetupSnapshotAcceptsOptionalValidROI(t *testing.T) {
	snapshot := testSetupSnapshot()
	if err := validateSetupSnapshot(snapshot, nil); err != nil {
		t.Fatalf("legacy snapshot without ROI rejected: %v", err)
	}
	snapshot.ROI = &SetupROI{
		Frame: "level_forward_lateral_up",
		MinM:  [3]float64{0.5, -1.5, 0},
		MaxM:  [3]float64{5.5, 1.5, 2.2},
	}
	if err := validateSetupSnapshot(snapshot, nil); err != nil {
		t.Fatalf("valid ROI rejected: %v", err)
	}
	validROI := *snapshot.ROI
	for _, mutate := range []func(*SetupROI){
		func(roi *SetupROI) { roi.Frame = "sensor_xyz" },
		func(roi *SetupROI) { roi.MinM[0] = -0.1 },
		func(roi *SetupROI) { roi.MinM[2] = -0.1 },
		func(roi *SetupROI) { roi.MaxM[1] = roi.MinM[1] },
		func(roi *SetupROI) { roi.MaxM[0] = math.Inf(1) },
	} {
		invalid := validROI
		mutate(&invalid)
		snapshot.ROI = &invalid
		if err := validateSetupSnapshot(snapshot, nil); err == nil {
			t.Fatalf("invalid ROI accepted: %+v", invalid)
		}
	}
}

func testSetupSnapshot() SetupSnapshot {
	return SetupSnapshot{
		Schema: SetupSchema,
		Radar: SetupRadar{
			Model: "iwr6843", Revision: "es2", Port: "COM3", D2XX: "AR-DevPack-EVM-012",
			BSS: SetupFile{Name: "xwr68xx_radarss.bin", Bytes: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			MSS: SetupFile{Name: "xwr68xx_masterss.bin", Bytes: 1, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		DCA:   SetupDCA{Host: "192.168.33.30", Device: "192.168.33.180", DelayUS: 50},
		Mount: SetupMount{HeightM: 1.5, PitchDeg: 0},
	}
}

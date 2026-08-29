package take

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mmwcli/internal/radar"
)

func TestRadarOnlyTakePublishesFlatFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := filepath.Join(t.TempDir(), "take")
	capture, err := New(ctx, cancel, Config{
		Output:      output,
		RadarConfig: []byte("frameCfg fixture\n"),
		Plan: radar.Plan{
			NumberOfFrames: 1,
			FramePeriod:    time.Millisecond,
			BytesPerFrame:  4,
			ExpectedBytes:  4,
		},
		RadarHeightM: 1.5,
		RadarTiltDeg: 90,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
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
	if len(entries) != 3 {
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
	if record.Schema != Schema || record.FrameCount != 1 || record.RadarTiltDeg != 90 || record.Camera != nil {
		t.Fatalf("unexpected manifest: %+v", record)
	}
}

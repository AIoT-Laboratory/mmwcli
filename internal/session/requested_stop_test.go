package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/dca"
)

func TestInfiniteCaptureRequestedStopPublishesCompleteFrames(t *testing.T) {
	plan := sessionTestPlan(t)
	plan.InfiniteFrames = true
	plan.NumberOfFrames = 0
	plan.ExpectedBytes = 0
	plan.BytesPerFrame = 3

	outputPath := filepath.Join(t.TempDir(), "capture.bin")
	output, err := capturefile.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	stopRequested := make(chan struct{})
	waitCalls := 0
	receiver := &fakeReceiver{
		events:  &events,
		payload: []byte("adc"),
		stats: dca.CaptureStats{
			PacketsReceived: 1,
			OutputBytes:     3,
		},
		waitHook: func(ctx context.Context) (dca.CaptureStats, error) {
			waitCalls++
			if waitCalls == 1 {
				close(stopRequested)
				<-ctx.Done()
				return dca.CaptureStats{PacketsReceived: 1, OutputBytes: 3}, ctx.Err()
			}
			return dca.CaptureStats{PacketsReceived: 1, OutputBytes: 3}, nil
		},
	}
	options := DefaultOptions()
	options.StopRequested = stopRequested

	stats, err := Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
		plan,
		output,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OutputBytes != 3 {
		t.Fatalf("output bytes = %d, want 3", stats.OutputBytes)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("gracefully stopped output was not committed: %v", err)
	}
}

func TestInfiniteCaptureRejectsPartialFrameAfterRequestedStop(t *testing.T) {
	plan := sessionTestPlan(t)
	plan.InfiniteFrames = true
	plan.NumberOfFrames = 0
	plan.ExpectedBytes = 0
	plan.BytesPerFrame = 3
	stats := dca.CaptureStats{PacketsReceived: 1, OutputBytes: 4}

	err := validateResult(plan, stats, time.Second, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "partial frame") {
		t.Fatalf("validateResult error = %v, want partial-frame failure", err)
	}
}

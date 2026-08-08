package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
	"mmwcli/internal/radar"
)

type fakeAggregateCoordinator struct {
	result   multisensorcapture.Result
	finishes []bool
}

func (*fakeAggregateCoordinator) Arm(context.Context) error   { return nil }
func (*fakeAggregateCoordinator) Start(context.Context) error { return nil }
func (coordinator *fakeAggregateCoordinator) Finish(_ context.Context, complete bool) error {
	coordinator.finishes = append(coordinator.finishes, complete)
	return nil
}
func (coordinator *fakeAggregateCoordinator) Result() (multisensorcapture.Result, error) {
	return coordinator.result, nil
}

func TestCaptureCommandsExposeMultisensorPlanAndRejectStreamCombination(t *testing.T) {
	for _, command := range []string{"studio-cli", "debug-cli"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run([]string{command, "capture", "--help"}, &stdout, &stderr); code != 0 {
				t.Fatalf("help exit = %d: %s%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), "multisensor-plan") {
				t.Fatalf("capture help omits --multisensor-plan: %s%s", stdout.String(), stderr.String())
			}
			stdout.Reset()
			stderr.Reset()
			arguments := []string{
				command, "capture", "capture.cfg", "capture-output",
				"--stream", "--multisensor-plan", "sensors.json",
			}
			if code := Run(arguments, &stdout, &stderr); code != 2 {
				t.Fatalf("mutual-exclusion exit = %d", code)
			}
			if !strings.Contains(stderr.String(), "mutually exclusive") {
				t.Fatalf("mutual-exclusion error = %s", stderr.String())
			}
		})
	}
}

func TestActiveMultisensorCapturePublishesRadarAggregateAfterParticipantFinish(t *testing.T) {
	prepared, err := loadCaptureOutputPlan(radar.StudioCLI, writeValidConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "aggregate")
	directory, err := multisensor.CreateDirectory(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	radarPath, err := directory.SourcePath(aggregateRadarSourceID)
	if err != nil {
		t.Fatal(err)
	}
	radarDirectory, err := createCaptureOutput(radarPath, prepared.finalizeSession)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, int(prepared.plan.ExpectedBytes))
	if written, err := radarDirectory.WriteAt(payload, 0); err != nil || written != len(payload) {
		t.Fatalf("write radar payload = %d, %v", written, err)
	}
	if err := radarDirectory.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessionID, err := multisensor.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeAggregateCoordinator{result: multisensorcapture.Result{
		PlanSchema: multisensorcapture.PlanSchema, SessionID: sessionID,
		SynchronizationGrade: multisensor.SynchronizationSoftwareBarrier,
		HostClock: multisensor.Clock{
			ClockID: multisensor.HostClockID, TickHz: 1_000_000_000,
			TimestampSemantics: multisensor.TimestampHostMonotonic,
		},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
		Sources:             []multisensor.Source{},
	}}
	origin := time.Now()
	capture := &activeMultisensorCapture{
		directory: directory, radarDirectory: radarDirectory, coordinator: coordinator,
		sessionID: sessionID, prepared: prepared, hostOrigin: origin,
	}
	capture.RadarFrameStartObserved(origin.Add(10*time.Millisecond), origin.Add(12*time.Millisecond))
	if err := capture.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := capture.finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !directory.Committed() || len(coordinator.finishes) != 1 || !coordinator.finishes[0] {
		t.Fatalf("aggregate completion = committed:%v finishes:%v", directory.Committed(), coordinator.finishes)
	}
	encoded, err := os.ReadFile(filepath.Join(outputPath, multisensor.SessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := multisensor.UnmarshalSession(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionRecord.Sources) != 1 || sessionRecord.Sources[0].SourceID != aggregateRadarSourceID {
		t.Fatalf("aggregate sources = %+v", sessionRecord.Sources)
	}
	observation := sessionRecord.Sources[0].ClockObservations[0]
	if observation.HostBeforeNS != uint64(10*time.Millisecond) ||
		observation.HostAfterNS != uint64(12*time.Millisecond) {
		t.Fatalf("radar frame-start observation = %+v", observation)
	}
	for _, name := range []string{"adc.bin", "capture.json", "index.bin", "radar.cfg"} {
		if _, err := os.Stat(filepath.Join(outputPath, "sensors", aggregateRadarSourceID, name)); err != nil {
			t.Fatalf("aggregate radar artifact %q: %v", name, err)
		}
	}
}

func TestActiveMultisensorCaptureFailureAbortsParticipantAndRetainsStage(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "aggregate")
	directory, err := multisensor.CreateDirectory(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeAggregateCoordinator{}
	capture := &activeMultisensorCapture{directory: directory, coordinator: coordinator}
	wantErr := errors.New("radar failed")
	if err := capture.finish(context.Background(), wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v", err)
	}
	if len(coordinator.finishes) != 1 || coordinator.finishes[0] {
		t.Fatalf("participant finishes = %v", coordinator.finishes)
	}
	if _, err := os.Stat(outputPath + ".part"); err != nil {
		t.Fatalf("failed aggregate stage missing: %v", err)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed aggregate published: %v", err)
	}
}

package multisensor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturemanifest"
	"mmwcli/internal/radar"
)

const radarSourceTestConfig = `flushCfg
dfeDataOutputMode 1
channelCfg 1 1 0
adcCfg 2 1
adcbufCfg -1 0 1 1 1
profileCfg 0 60 7 3 24 0 0 166 1 16 12500 0 0 158
chirpCfg 0 0 0 0 0 0 0 1
frameCfg 0 0 1 3 10 1 0
lvdsStreamCfg -1 0 1 0
sensorStart
`

func TestFinalizeRadarSourcePublishesExactFrameIndexAndArtifacts(t *testing.T) {
	config, plan := radarSourceTestPlan(t)
	directory := committedRadarSourceDirectory(t, config, plan, plan.ExpectedBytes)
	bracket := FrameStartBracket{LowerNS: 1_000_000_000, UpperNS: 1_000_000_101}
	source, err := FinalizeRadarSource(
		context.Background(), directory.FinalPath(), config, plan, "1.2.3", bracket, "radar-0",
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceID != "radar-0" || source.Kind != SourceRadar || !source.Required ||
		source.Outcome != OutcomeComplete || source.Producer != (Producer{Name: "mmwcli", Version: "1.2.3"}) ||
		source.ItemCount != 3 || source.PayloadBytes != uint64(plan.ExpectedBytes) ||
		source.Clock.TickHz != 1_000_000_000 || source.Clock.TimestampSemantics != TimestampFrameStart {
		t.Fatalf("radar source = %+v", source)
	}
	observation := source.ClockObservations[0]
	segment := source.AffineSegments[0]
	if observation.HostBeforeNS != bracket.LowerNS || observation.HostAfterNS != bracket.UpperNS ||
		segment.HostOriginNS != bracket.LowerNS+(bracket.UpperNS-bracket.LowerNS)/2 ||
		segment.UncertaintyNS != 51 || segment.ScaleNum != 1 || segment.ScaleDen != 1 {
		t.Fatalf("clock evidence = observation:%+v segment:%+v", observation, segment)
	}
	encoded, err := os.ReadFile(filepath.Join(directory.FinalPath(), IndexFileName))
	if err != nil {
		t.Fatal(err)
	}
	index, err := DecodeSensorIndex(encoded, source.Limits)
	if err != nil {
		t.Fatal(err)
	}
	for item, entry := range index.Entries {
		if entry.ItemIndex != uint64(item) || entry.PayloadOffset != uint64(item)*uint64(plan.BytesPerFrame) ||
			entry.PayloadSize != uint64(plan.BytesPerFrame) || entry.Tick != uint64(item)*uint64(10*time.Millisecond) ||
			entry.DurationTicks != uint64(10*time.Millisecond) || entry.SyncEventID != NoSyncEventID {
			t.Fatalf("index entry %d = %+v", item, entry)
		}
	}
	entries, err := os.ReadDir(directory.FinalPath())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	wantNames := []string{"adc.bin", "capture.json", "index.bin", "radar.cfg"}
	if !reflect.DeepEqual(names, wantNames) || len(source.Artifacts) != len(wantNames) {
		t.Fatalf("source files/artifacts = %v/%+v", names, source.Artifacts)
	}
	if err := validateSourceIndex(source, index, map[uint64]SyncEvent{}); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeRadarSourceDerivesCompletedInfiniteFrameCount(t *testing.T) {
	config, _ := radarSourceTestPlan(t)
	config = bytes.Replace(config, []byte("frameCfg 0 0 1 3 10 1 0"), []byte("frameCfg 0 0 1 0 10 1 0"), 1)
	plan, err := radar.BuildCaptureSessionV1Plan(config, radar.FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InfiniteFrames || plan.BytesPerFrame != 64 {
		t.Fatalf("infinite test plan = %+v", plan)
	}

	directory := committedRadarSourceDirectory(t, config, plan, 4*plan.BytesPerFrame)
	source, err := FinalizeRadarSource(
		context.Background(),
		directory.FinalPath(),
		config,
		plan,
		"1.2.3",
		FrameStartBracket{LowerNS: 1, UpperNS: 2},
		"radar-0",
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.ItemCount != 4 || source.PayloadBytes != 4*uint64(plan.BytesPerFrame) {
		t.Fatalf("finalized infinite source = %+v", source)
	}
}

func TestFinalizeRadarSourceRejectsPartialInfiniteFrame(t *testing.T) {
	config, _ := radarSourceTestPlan(t)
	config = bytes.Replace(config, []byte("frameCfg 0 0 1 3 10 1 0"), []byte("frameCfg 0 0 1 0 10 1 0"), 1)
	plan, err := radar.BuildCaptureSessionV1Plan(config, radar.FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	directory := committedRadarSourceDirectory(t, config, plan, 3*plan.BytesPerFrame+2)

	_, err = FinalizeRadarSource(
		context.Background(),
		directory.FinalPath(),
		config,
		plan,
		"1.2.3",
		FrameStartBracket{LowerNS: 1, UpperNS: 2},
		"radar-0",
	)
	if err == nil || !strings.Contains(err.Error(), "whole number") {
		t.Fatalf("partial-frame error = %v", err)
	}
}

func TestFinalizeRadarSourceRejectsUncommittedSizeMismatchAndBadInterval(t *testing.T) {
	config, plan := radarSourceTestPlan(t)
	t.Run("uncommitted", func(t *testing.T) {
		finalizer, err := capturemanifest.NewV1Finalizer(config, plan.RawCapture)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := capturefile.CreateSessionDirectory(filepath.Join(t.TempDir(), "radar-0"), finalizer)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = directory.Close() })
		if _, err := FinalizeRadarSource(
			context.Background(), directory.PartPath(), config, plan, "1.2.3",
			FrameStartBracket{LowerNS: 1, UpperNS: 2}, "radar-0",
		); err == nil || !strings.Contains(err.Error(), "not a committed") {
			t.Fatalf("uncommitted error = %v", err)
		}
	})

	t.Run("ADC size", func(t *testing.T) {
		directory := committedRadarSourceDirectory(t, config, plan, plan.ExpectedBytes-plan.BytesPerFrame)
		if _, err := FinalizeRadarSource(
			context.Background(), directory.FinalPath(), config, plan, "1.2.3",
			FrameStartBracket{LowerNS: 1, UpperNS: 2}, "radar-0",
		); err == nil || !strings.Contains(err.Error(), "ADC size") {
			t.Fatalf("size error = %v", err)
		}
	})

	t.Run("bad interval", func(t *testing.T) {
		directory := committedRadarSourceDirectory(t, config, plan, plan.ExpectedBytes)
		if _, err := FinalizeRadarSource(
			context.Background(), directory.FinalPath(), config, plan, "1.2.3",
			FrameStartBracket{LowerNS: 3, UpperNS: 2}, "radar-0",
		); err == nil || !strings.Contains(err.Error(), "lower bound") {
			t.Fatalf("interval error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(directory.FinalPath(), IndexFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bad interval wrote index.bin: %v", err)
		}
	})

	t.Run("configuration snapshot", func(t *testing.T) {
		directory := committedRadarSourceDirectory(t, config, plan, plan.ExpectedBytes)
		changed := bytes.Replace(config, []byte("sensorStart"), []byte("sensorStart 0"), 1)
		if _, err := FinalizeRadarSource(
			context.Background(), directory.FinalPath(), changed, plan, "1.2.3",
			FrameStartBracket{LowerNS: 1, UpperNS: 2}, "radar-0",
		); err == nil || !strings.Contains(err.Error(), "plan") {
			t.Fatalf("configuration error = %v", err)
		}
	})
}

func radarSourceTestPlan(t *testing.T) ([]byte, radar.CapturePlan) {
	t.Helper()
	config := []byte(radarSourceTestConfig)
	family, err := radar.ParseDeviceFamily("xwr68xx")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := radar.BuildCaptureSessionV1PlanForFamily(family, config)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BytesPerFrame != 64 || plan.ExpectedBytes != 192 || plan.NumberOfFrames != 3 {
		t.Fatalf("test radar plan geometry = %+v", plan)
	}
	return config, plan
}

func committedRadarSourceDirectory(
	t *testing.T,
	config []byte,
	plan radar.CapturePlan,
	size int64,
) *capturefile.SessionDirectory {
	t.Helper()
	finalizer, err := capturemanifest.NewV1Finalizer(config, plan.RawCapture)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := capturefile.CreateSessionDirectory(filepath.Join(t.TempDir(), "radar-0"), finalizer)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0xa5}, int(size))
	if written, err := directory.WriteAt(payload, 0); err != nil || written != len(payload) {
		t.Fatalf("write test ADC = %d, %v", written, err)
	}
	if err := directory.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return directory
}

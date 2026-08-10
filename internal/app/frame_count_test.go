package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

func TestStudioCLICheckFrameCountOverride(t *testing.T) {
	config := writeValidConfig(t)
	for _, test := range []struct {
		count string
		want  string
	}{
		{count: "0", want: "frames=infinite"},
		{count: "7", want: "frames=7"},
	} {
		t.Run(test.count, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(
				[]string{"studio-cli", "check", config, "--frame-count", test.count},
				&stdout,
				&stderr,
			); code != 0 {
				t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("check output = %q, want %q", stdout.String(), test.want)
			}
		})
	}
}

func TestStudioCLICaptureValidatesGracefulStopFlagsBeforeOutput(t *testing.T) {
	config := writeValidConfig(t)
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{
			name: "infinite requires stop channel",
			arguments: []string{
				"studio-cli", "capture", config, "OUTPUT", "--port", "COM3", "--frame-count", "0",
			},
			want: "requires --stop-on-stdin-eof",
		},
		{
			name: "finite rejects stop channel",
			arguments: []string{
				"studio-cli", "capture", config, "OUTPUT", "--port", "COM3", "--stop-on-stdin-eof",
			},
			want: "valid only when the effective frame count is 0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture")
			arguments := append([]string(nil), test.arguments...)
			for index, argument := range arguments {
				if argument == "OUTPUT" {
					arguments[index] = output
				}
			}
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("argument failure created output: %v", err)
			}
		})
	}
}

func TestLoadCaptureOutputPlanArchivesOverriddenFrameCount(t *testing.T) {
	config := writeValidConfig(t)
	count := uint16(0)
	prepared, err := loadCaptureOutputPlanWithFrameCount(radar.StudioCLI, config, &count)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.plan.InfiniteFrames || !strings.Contains(
		string(prepared.configSnapshot),
		"frameCfg 0 0 1 0 10 1 0",
	) {
		t.Fatalf("effective capture plan was not archived:\n%s", prepared.configSnapshot)
	}
}

func TestCaptureDurationControlsAllowOnlyOpenEndedAggregateStream(t *testing.T) {
	config := writeValidConfig(t)
	finite, err := loadCaptureOutputPlan(radar.StudioCLI, config)
	if err != nil {
		t.Fatal(err)
	}
	count := uint16(0)
	openEnded, err := loadCaptureOutputPlanWithFrameCount(radar.StudioCLI, config, &count)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		plan        radar.CapturePlan
		stop        bool
		stream      bool
		multisensor string
		match       string
	}{
		{name: "missing stop", plan: openEnded.plan, match: "requires --stop-on-stdin-eof"},
		{
			name: "radar-only stream", plan: openEnded.plan, stop: true, stream: true,
			match: "requires --multisensor-plan",
		},
		{
			name: "aggregate stream", plan: openEnded.plan, stop: true, stream: true,
			multisensor: "camera.json",
		},
		{name: "finite stop", plan: finite.plan, stop: true, match: "valid only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCaptureDurationControls(test.plan, test.stop, test.stream, test.multisensor)
			if test.match == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("duration control error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestConstrainCaptureOutputBytesUsesExactFiniteAndWholeFrameOpenEndedBounds(t *testing.T) {
	config := writeValidConfig(t)
	finite, err := loadCaptureOutputPlan(radar.StudioCLI, config)
	if err != nil {
		t.Fatal(err)
	}
	finiteReceiver := dca.ReceiverConfig{MaxOutputBytes: finite.plan.ExpectedBytes + 1}
	if err := constrainCaptureOutputBytes(finite.plan, &finiteReceiver); err != nil {
		t.Fatal(err)
	}
	if finiteReceiver.MaxOutputBytes != finite.plan.ExpectedBytes {
		t.Fatalf("finite maximum bytes = %d", finiteReceiver.MaxOutputBytes)
	}

	count := uint16(0)
	openEnded, err := loadCaptureOutputPlanWithFrameCount(radar.StudioCLI, config, &count)
	if err != nil {
		t.Fatal(err)
	}
	tooSmall := dca.ReceiverConfig{MaxOutputBytes: openEnded.plan.BytesPerFrame - 1}
	if err := constrainCaptureOutputBytes(openEnded.plan, &tooSmall); err == nil ||
		!strings.Contains(err.Error(), "smaller than one CFG-derived radar frame") {
		t.Fatalf("open-ended byte bound error = %v", err)
	}
	wholeFrames := dca.ReceiverConfig{MaxOutputBytes: 3 * openEnded.plan.BytesPerFrame}
	if err := constrainCaptureOutputBytes(openEnded.plan, &wholeFrames); err != nil {
		t.Fatal(err)
	}
	if wholeFrames.MaxOutputBytes != 3*openEnded.plan.BytesPerFrame {
		t.Fatalf("open-ended maximum bytes = %d", wholeFrames.MaxOutputBytes)
	}
}

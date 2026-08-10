package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

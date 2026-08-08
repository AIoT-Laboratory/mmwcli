package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mmwcli/internal/fixedframeproducer"
	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
)

func TestMultisensorCheckPrintsStrictPlanOffline(t *testing.T) {
	planPath := writeMultisensorCheckPlan(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"multisensor", "check", planPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	for _, expected := range []string{
		"source[0]: id=camera-required kind=camera required=true producer=camera-producer@1.2.3 payload=required.bin format=camera.rgb8.v1 clock=required-clock tick_hz=1000000 wrap_ticks=0 timestamp_semantics=exposure_midpoint max_items=4 max_item_bytes=16 max_payload_bytes=64",
		"source[1]: id=camera-optional kind=camera required=false producer=optional-producer@2.0 payload=optional.bin format=camera.jpeg.v1 clock=optional-clock tick_hz=90000 wrap_ticks=4294967296 timestamp_semantics=exposure_midpoint max_items=8 max_item_bytes=1024 max_payload_bytes=8192",
		"sources: total=2 required=1 optional=1",
		"multisensor plan check passed (offline; no processes or hardware accessed)",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), expected)
		}
	}
}

func TestMultisensorInitCreatesRunnableFixedFramePlan(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "camera-plan.json")
	arguments := []string{
		"multisensor", "init", planPath,
		"--format", "camera.rgb8.v1", "--frame-bytes", "4", "--max-items", "3",
		"--", "ffmpeg", "-f", "rawvideo", "-",
	}
	var stdout, stderr bytes.Buffer
	if code := Run(arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("Run code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	plan, err := multisensorcapture.LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sources) != 1 {
		t.Fatalf("sources = %d", len(plan.Sources))
	}
	source := plan.Sources[0]
	if source.SourceID != "camera-0" || source.Kind != multisensor.SourceCamera || !source.Required ||
		source.QueueSize != 0 || source.Producer.Name != fixedframeproducer.ProducerName ||
		source.Producer.Version != fixedframeproducer.ProducerVersion ||
		source.Limits != (multisensor.SourceLimits{MaxItems: 3, MaxItemBytes: 4, MaxPayloadBytes: 12}) ||
		source.Payload != (multisensor.PayloadContract{Filename: "frames.bin", Format: "camera.rgb8.v1"}) ||
		source.Clock.ClockID != multisensor.DeliveryObservedClockID("camera-0") ||
		source.Clock.TickHz != 1_000_000_000 || source.Clock.WrapTicks != 0 ||
		source.Clock.TimestampSemantics != multisensor.TimestampDeliveryObserved ||
		source.SyncEventSemantics != multisensorcapture.SyncEventSemanticsNone {
		t.Fatalf("source = %+v", source)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{
		executable, "sensor-producer", "fixed-frames", "--plan", planPath,
		"--source", "camera-0", "--frame-bytes", "4", "--",
		"ffmpeg", "-f", "rawvideo", "-",
	}
	if strings.Join(source.Argv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("argv = %#v, want %#v", source.Argv, wantArgv)
	}
	for _, expected := range []string{
		"created multisensor plan: " + planPath,
		"mmwcli multisensor check " + planPath,
		"mmwcli studio-cli capture CFG OUTDIR --port PORT --multisensor-plan " + planPath,
		"mmwcli debug-cli capture CFG OUTDIR --family FAMILY",
		"--multisensor-plan " + planPath,
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), expected)
		}
	}
}

func TestMultisensorInitSupportsOptionalSourceAndNeverOverwrites(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "optional-plan.json")
	arguments := []string{
		"multisensor", "init", planPath,
		"--source-id", "camera-left", "--format", "camera.gray8.v1",
		"--frame-bytes", "8", "--max-items", "2", "--required=false",
		"--payload", "left.bin", "--", "vendor-camera", "--raw",
	}
	var stdout, stderr bytes.Buffer
	if code := Run(arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("Run code = %d, stderr = %q", code, stderr.String())
	}
	plan, err := multisensorcapture.LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	source := plan.Sources[0]
	if source.Required || source.SourceID != "camera-left" || source.Payload.Filename != "left.bin" ||
		source.Limits.MaxPayloadBytes != 16 {
		t.Fatalf("source = %+v", source)
	}
	original, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(arguments, &stdout, &stderr); code != 4 {
		t.Fatalf("second Run code = %d, stderr = %q", code, stderr.String())
	}
	after, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("existing plan was changed")
	}
}

func TestMultisensorInitRejectsMissingCommandAndOverflow(t *testing.T) {
	tests := [][]string{
		{"multisensor", "init", filepath.Join(t.TempDir(), "missing-command.json"),
			"--format", "camera.raw.v1", "--frame-bytes", "4", "--max-items", "2"},
		{"multisensor", "init", filepath.Join(t.TempDir(), "overflow.json"),
			"--format", "camera.raw.v1", "--frame-bytes", "67108864",
			"--max-items", "18446744073709551615", "--", "camera"},
	}
	for _, arguments := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("Run(%v) code = %d, stderr = %q", arguments, code, stderr.String())
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "argument error:") {
			t.Fatalf("Run(%v) stdout=%q stderr=%q", arguments, stdout.String(), stderr.String())
		}
	}
}

func TestMultisensorCheckHelpAndRootRouting(t *testing.T) {
	tests := []struct {
		arguments []string
		want      string
	}{
		{arguments: []string{"help"}, want: "mmwcli multisensor check PLAN"},
		{arguments: []string{"multisensor", "--help"}, want: "usage: mmwcli multisensor check PLAN"},
		{arguments: []string{"multisensor", "init", "--help"}, want: "mmwcli multisensor init PLAN"},
		{arguments: []string{"multisensor", "check", "--help"}, want: "usage: mmwcli multisensor check PLAN"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(test.arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%v) code = %d, stderr = %q", test.arguments, code, stderr.String())
		}
		output := stdout.String() + stderr.String()
		if !strings.Contains(output, test.want) {
			t.Fatalf("Run(%v) output = %q, want %q", test.arguments, output, test.want)
		}
	}
}

func TestMultisensorCheckHasNoDefaultOrAlias(t *testing.T) {
	planPath := writeMultisensorCheckPlan(t)
	tests := [][]string{
		{"multisensor"},
		{"multisensor", planPath},
		{"multisensor", "validate", planPath},
		{"multisensor", "check"},
		{"multisensor", "check", planPath, planPath},
	}
	for _, arguments := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("Run(%v) code = %d, stderr = %q", arguments, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("Run(%v) stdout = %q", arguments, stdout.String())
		}
		if !strings.Contains(stderr.String(), "argument error:") {
			t.Fatalf("Run(%v) stderr = %q", arguments, stderr.String())
		}
	}
}

func TestMultisensorCheckRejectsInvalidPlanWithoutOutput(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "invalid.json")
	encoded := `{"schema":"mmwcli.multisensor_plan.v1","sources":[],"application_metadata":{},"unknown":true}`
	if err := os.WriteFile(planPath, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"multisensor", "check", planPath}, &stdout, &stderr); code != 4 {
		t.Fatalf("Run code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func writeMultisensorCheckPlan(t *testing.T) string {
	t.Helper()
	plan := multisensorcapture.Plan{
		Schema: multisensorcapture.PlanSchema,
		Sources: []multisensorcapture.SourcePlan{
			{
				SourceID: "camera-required", Kind: multisensor.SourceCamera, Required: true,
				Argv: []string{"camera-producer", "--stdio"}, QueueSize: 4,
				Producer: multisensor.Producer{Name: "camera-producer", Version: "1.2.3"},
				Limits:   multisensor.SourceLimits{MaxItems: 4, MaxItemBytes: 16, MaxPayloadBytes: 64},
				Payload:  multisensor.PayloadContract{Filename: "required.bin", Format: "camera.rgb8.v1"},
				Clock: multisensor.Clock{
					ClockID: "required-clock", TickHz: 1_000_000,
					TimestampSemantics: multisensor.TimestampExposureMidpoint,
				},
				SyncEventSemantics:  multisensorcapture.SyncEventSemanticsNone,
				ApplicationMetadata: multisensor.ApplicationMetadata{},
			},
			{
				SourceID: "camera-optional", Kind: multisensor.SourceCamera, Required: false,
				Argv: []string{"optional-producer"}, QueueSize: 1,
				Producer: multisensor.Producer{Name: "optional-producer", Version: "2.0"},
				Limits:   multisensor.SourceLimits{MaxItems: 8, MaxItemBytes: 1024, MaxPayloadBytes: 8192},
				Payload:  multisensor.PayloadContract{Filename: "optional.bin", Format: "camera.jpeg.v1"},
				Clock: multisensor.Clock{
					ClockID: "optional-clock", TickHz: 90_000, WrapTicks: 1 << 32,
					TimestampSemantics: multisensor.TimestampExposureMidpoint,
				},
				SyncEventSemantics:  multisensorcapture.SyncEventSemanticsNone,
				ApplicationMetadata: multisensor.ApplicationMetadata{},
			},
		},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "multisensor-plan.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

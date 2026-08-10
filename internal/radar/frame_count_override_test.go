package radar

import (
	"strings"
	"testing"
)

func TestOverrideCaptureSessionV1FrameCountProducesEffectiveSnapshot(t *testing.T) {
	snapshot := []byte(strings.Replace(
		string(renderSessionConfig(validCommands())),
		"frameCfg 0 1 32 100 100 1 0",
		"  frameCfg 0 1 32 100 100 1 0",
		1,
	))

	effective, err := OverrideCaptureSessionV1FrameCount(snapshot, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(effective), "  frameCfg 0 1 32 0 100 1 0") {
		t.Fatalf("effective CFG does not contain the overridden frame count:\n%s", effective)
	}
	plan, err := BuildCaptureSessionV1Plan(effective, FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InfiniteFrames || plan.NumberOfFrames != 0 || plan.ExpectedBytes != 0 {
		t.Fatalf("infinite plan = %+v", plan)
	}

	effective, err = OverrideCaptureSessionV1FrameCount(snapshot, 7)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = BuildCaptureSessionV1Plan(effective, FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	if plan.InfiniteFrames || plan.NumberOfFrames != 7 || plan.ExpectedBytes != 7*plan.BytesPerFrame {
		t.Fatalf("finite override plan = %+v", plan)
	}
}

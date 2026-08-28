package radar

import (
	"strings"
	"testing"
)

func TestSetFrameCountProducesEffectiveSnapshot(t *testing.T) {
	snapshot := []byte(strings.Replace(
		string(renderSessionConfig(validCommands())),
		"frameCfg 0 1 32 100 100 1 0",
		"  frameCfg 0 1 32 100 100 1 0",
		1,
	))

	if _, err := SetFrameCount(snapshot, 0); err == nil {
		t.Fatal("zero frame override was accepted")
	}

	effective, err := SetFrameCount(snapshot, 7)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(effective)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NumberOfFrames != 7 || plan.ExpectedBytes != 7*plan.BytesPerFrame {
		t.Fatalf("finite override plan = %+v", plan)
	}
}

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

	continuous, err := SetFrameCount(snapshot, 0)
	if err != nil {
		t.Fatal(err)
	}
	continuousPlan, err := BuildPlan(continuous)
	if err != nil {
		t.Fatal(err)
	}
	if continuousPlan.NumberOfFrames != 0 || continuousPlan.ExpectedBytes != 0 {
		t.Fatalf("continuous override plan = %+v", continuousPlan)
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

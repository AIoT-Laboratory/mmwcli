package radar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanAcceptsRepositoryConfig(t *testing.T) {
	tests := []struct {
		name          string
		bytesPerFrame int64
		expectedBytes int64
	}{
		{name: "iwr6843.cfg", bytesPerFrame: 1_572_864, expectedBytes: 943_718_400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := os.ReadFile(filepath.Join("..", "..", "hardware", test.name))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := BuildPlan(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if plan.ExpectedBytes != test.expectedBytes || plan.BytesPerFrame != test.bytesPerFrame {
				t.Fatalf("plan sizes = frame %d total %d", plan.BytesPerFrame, plan.ExpectedBytes)
			}
		})
	}
}

func TestValidatePlanBindsExactSemantics(t *testing.T) {
	snapshot := renderSessionConfig(validCommands())
	plan, err := BuildPlan(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePlan(snapshot, plan); err != nil {
		t.Fatalf("validate exact plan: %v", err)
	}

	t.Run("tampered plan geometry", func(t *testing.T) {
		tampered := plan
		tampered.BytesPerFrame--
		if err := ValidatePlan(snapshot, tampered); err == nil {
			t.Fatal("tampered capture plan was accepted")
		}
	})

	t.Run("tampered plan command", func(t *testing.T) {
		tampered := plan
		tampered.ConfigurationCommands = replaceCommand(
			plan.ConfigurationCommands,
			"profileCfg",
			"profileCfg 0 61 7 3 24 0 0 166 1 256 12500 0 0 158",
		)
		if err := ValidatePlan(snapshot, tampered); err == nil {
			t.Fatal("capture plan with different physical commands was accepted")
		}
	})

	t.Run("different CFG with same geometry", func(t *testing.T) {
		alternate := replaceCommand(
			validCommands(),
			"profileCfg",
			"profileCfg 0 61 7 3 24 0 0 166 1 256 12500 0 0 158",
		)
		alternateSnapshot := renderSessionConfig(alternate)
		alternatePlan, err := BuildPlan(alternateSnapshot)
		if err != nil {
			t.Fatalf("build alternate capture plan: %v", err)
		}
		if !sameGeometry(alternatePlan, plan) {
			t.Fatal("alternate capture plan did not preserve byte geometry")
		}
		if err := ValidatePlan(alternateSnapshot, plan); err == nil {
			t.Fatal("different physical CFG with equal byte geometry was accepted")
		}
	})
}

func TestBuildPlanUsesMmwcoreCommentBoundary(t *testing.T) {
	commands := append([]string{"// ignored by the offline contract parser"}, validCommands()...)
	if _, err := BuildPlan(renderSessionConfig(commands)); err != nil {
		t.Fatal(err)
	}
	commands = replaceCommand(commands[1:], "adcCfg", "adcCfg 2 1 // not an mmwcore inline comment")
	if _, err := BuildPlan(renderSessionConfig(commands)); err == nil {
		t.Fatal("inline // produced a session CFG that mmwcore cannot parse")
	}
}

func TestBuildPlanRejectsInvalidUTF8(t *testing.T) {
	snapshot := append([]byte("% ignored comment "), 0xff)
	snapshot = append(snapshot, '\n')
	snapshot = append(snapshot, renderSessionConfig(validCommands())...)
	if _, err := BuildPlan(snapshot); err == nil ||
		!strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error = %v, want UTF-8 rejection", err)
	}
}

func TestBuildPlanRejectsUnrepresentableConfigs(t *testing.T) {
	tests := []struct {
		name        string
		replacement string
		match       string
	}{
		{name: "alternate complex format", replacement: "adcCfg 2 2", match: "exact adcCfg 2 1"},
		{name: "sparse RX", replacement: "channelCfg 5 7 0", match: "sparse"},
		{name: "simultaneous TX", replacement: "chirpCfg 0 0 0 0 0 0 0 3", match: "exactly one TX"},
		{name: "repeated TX", replacement: "chirpCfg 1 1 0 0 0 0 0 1", match: "each active TX exactly once"},
		{name: "chirp variation", replacement: "chirpCfg 0 0 0 1 0 0 0 1", match: "chirpCfg variations"},
		{name: "hex chirp variation", replacement: "chirpCfg 0 0 0 0x0p0 0 0 0 1", match: "decimal floating-point"},
		{name: "zero start frequency", replacement: "profileCfg 0 0 7 3 24 0 0 166 1 256 12500 0 0 158", match: "start frequency"},
		{name: "NaN start frequency", replacement: "profileCfg 0 NaN 7 3 24 0 0 166 1 256 12500 0 0 158", match: "finite decimal"},
		{name: "hex start frequency", replacement: "profileCfg 0 0x1p5 7 3 24 0 0 166 1 256 12500 0 0 158", match: "decimal floating-point"},
		{name: "scaled start overflow", replacement: "profileCfg 0 1e300 7 3 24 0 0 166 1 256 12500 0 0 158", match: "scaled profileCfg start frequency"},
		{name: "zero idle time", replacement: "profileCfg 0 60 0 3 24 0 0 166 1 256 12500 0 0 158", match: "idle time"},
		{name: "infinite idle time", replacement: "profileCfg 0 60 Inf 3 24 0 0 166 1 256 12500 0 0 158", match: "finite decimal"},
		{name: "scaled idle underflow", replacement: "profileCfg 0 60 5e-324 3 24 0 0 166 1 256 12500 0 0 158", match: "scaled profileCfg idle time"},
		{name: "scaled ADC times equal", replacement: "profileCfg 0 60 7 5e-318 6e-318 0 0 166 1 256 12500 0 0 158", match: "precede ramp"},
		{name: "zero ADC start", replacement: "profileCfg 0 60 7 0 24 0 0 166 1 256 12500 0 0 158", match: "ADC start time"},
		{name: "ADC start after ramp", replacement: "profileCfg 0 60 7 24 24 0 0 166 1 256 12500 0 0 158", match: "precede ramp"},
		{name: "zero slope", replacement: "profileCfg 0 60 7 3 24 0 0 0 1 256 12500 0 0 158", match: "frequency slope"},
		{name: "zero sample rate", replacement: "profileCfg 0 60 7 3 24 0 0 166 1 256 0 0 0 158", match: "sample rate"},
		{name: "odd GROUP2 samples", replacement: "profileCfg 0 60 7 3 24 0 0 166 1 255 12500 0 0 158", match: "even"},
		{name: "short frame period", replacement: "frameCfg 0 1 32 100 1 1 0", match: "active chirp time"},
		{name: "hex frame period", replacement: "frameCfg 0 1 32 100 0x1p7 1 0", match: "decimal floating-point"},
		{name: "hex frame delay", replacement: "frameCfg 0 1 32 100 100 1 0x0p0", match: "decimal floating-point"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := validCommands()
			if test.name == "repeated TX" {
				commands = replaceCommandsByName(
					commands,
					"chirpCfg",
					"chirpCfg 0 0 0 0 0 0 0 1",
					"chirpCfg 1 1 0 0 0 0 0 1",
				)
			} else {
				command := strings.Fields(test.replacement)[0]
				commands = replaceCommand(commands, command, test.replacement)
			}
			_, err := BuildPlan(renderSessionConfig(commands))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.match)) {
				t.Fatalf("error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func renderSessionConfig(commands []string) []byte {
	return []byte(strings.Join(commands, "\n") + "\n")
}

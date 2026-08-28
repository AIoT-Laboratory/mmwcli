package radar

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseConfigComments(t *testing.T) {
	input := strings.Join([]string{
		"% percent comment",
		" # hash comment",
		"// slash comment",
		"",
		" flushCfg ",
		"adcCfg 2 1 // inline comment",
		"profileCfg 0 60//not-an-inline-comment",
	}, "\n")
	commands, err := ParseConfig(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"flushCfg", "adcCfg 2 1", "profileCfg 0 60//not-an-inline-comment"}
	if strings.Join(commands, "|") != strings.Join(want, "|") {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func validCommands() []string {
	return []string{
		"flushCfg",
		"dfeDataOutputMode 1",
		"channelCfg 15 7 0",
		"adcCfg 2 1",
		"adcbufCfg -1 0 1 1 1",
		"profileCfg 0 60 7 3 24 0 0 166 1 256 12500 0 0 158",
		"chirpCfg 0 0 0 0 0 0 0 1",
		"chirpCfg 1 1 0 0 0 0 0 4",
		"frameCfg 0 1 32 100 100 1 0",
		"lowPower 0 0",
		"lvdsStreamCfg -1 0 1 0",
		"sensorStart",
	}
}

func TestCommandPlan(t *testing.T) {
	plan, err := CommandPlan(validCommands())
	if err != nil {
		t.Fatal(err)
	}
	if plan.StartCommand != "sensorStart" || plan.DeclaredStartCommand != "sensorStart" || plan.StartWasSynthesized {
		t.Fatalf("unexpected start plan: %+v", plan)
	}
	if got := plan.ConfigurationCommands[len(plan.ConfigurationCommands)-1]; got != "lvdsStreamCfg -1 0 1 0" {
		t.Fatalf("configuration still contains start or lost LVDS: %q", got)
	}
	if plan.ExpectedDCADataFormat != 3 || !plan.HardwareLVDSEnabled {
		t.Fatalf("unexpected ADC/LVDS plan: %+v", plan)
	}
	if plan.BytesPerFrame != 262_144 || plan.ExpectedBytes != 26_214_400 {
		t.Fatalf("frame/total bytes = %d/%d, want 262144/26214400", plan.BytesPerFrame, plan.ExpectedBytes)
	}
	if plan.NumberOfFrames != 100 || plan.FramePeriod != 100*time.Millisecond {
		t.Fatalf("unexpected frame plan: %+v", plan)
	}
	span, err := plan.FrameSpan()
	if err != nil || span != 9900*time.Millisecond {
		t.Fatalf("FrameSpan = %s/%v", span, err)
	}
	maximum, err := plan.MaxDuration(1500 * time.Millisecond)
	if err != nil || maximum != 13900*time.Millisecond {
		t.Fatalf("MaxDuration = %s/%v", maximum, err)
	}
	maximum, err = plan.MaxDuration(2500 * time.Millisecond)
	if err != nil || maximum != 15900*time.Millisecond {
		t.Fatalf("tail-plus-quiet MaxDuration = %s/%v", maximum, err)
	}
}

func TestBuildPlanSynthesizesAndSeparatesStart(t *testing.T) {
	withoutStart := validCommands()[:len(validCommands())-1]
	plan, err := CommandPlan(withoutStart)
	if err != nil {
		t.Fatal(err)
	}
	if plan.StartCommand != "sensorStart" || !plan.StartWasSynthesized || plan.DeclaredStartCommand != "" {
		t.Fatalf("unexpected synthesized start: %+v", plan)
	}
}

func TestPlanRejectsNoReconfigureStart(t *testing.T) {
	commands := validCommands()
	commands[len(commands)-1] = "sensorStart 0"
	if _, err := CommandPlan(commands); err == nil {
		t.Fatal("plan accepted sensorStart 0")
	}
}

func TestZeroFramePlanIsRejected(t *testing.T) {
	commands := replaceCommand(validCommands(), "frameCfg", "frameCfg 0 1 32 0 100 1 0")
	if _, err := CommandPlan(commands); err == nil ||
		!strings.Contains(err.Error(), "frame count") {
		t.Fatalf("zero-frame error = %v", err)
	}
}

func TestExpectedBytesSingleReceiverSingleChirp(t *testing.T) {
	commands := replaceCommand(validCommands(), "channelCfg", "channelCfg 1 7 0")
	commands = replaceCommandsByName(commands, "chirpCfg", "chirpCfg 0 0 0 0 0 0 0 1")
	commands = replaceCommand(commands, "frameCfg", "frameCfg 0 0 1 1 100 1 0")

	plan, err := CommandPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ExpectedBytes != 1_024 {
		t.Fatalf("ExpectedBytes = %d, want 1024", plan.ExpectedBytes)
	}
}

func TestExpectedBytesRejectsAmbiguousOrIncompleteMapping(t *testing.T) {
	profile := "profileCfg 0 60 7 3 24 0 0 166 1 256 12500 0 0 158"
	tests := []struct {
		name   string
		mutate func([]string) []string
		match  string
	}{
		{name: "missing channel", mutate: removeCommand("channelCfg"), match: "exactly one unambiguous channelCfg"},
		{name: "duplicate channel", mutate: func(commands []string) []string {
			return replaceCommandsByName(commands, "channelCfg", "channelCfg 15 7 0", "channelCfg 15 7 0")
		}, match: "exactly one unambiguous channelCfg"},
		{name: "zero RX mask", mutate: replace("channelCfg", "channelCfg 0 7 0"), match: "RX mask"},
		{name: "RX mask outside xWR68xx", mutate: replace("channelCfg", "channelCfg 16 7 0"), match: "RX mask"},
		{name: "RX mask integer overflow", mutate: replace("channelCfg", "channelCfg 4294967296 7 0"), match: "invalid channelCfg RX mask"},
		{name: "zero TX mask", mutate: replace("channelCfg", "channelCfg 15 0 0"), match: "TX mask"},
		{name: "TX mask outside xWR68xx", mutate: replace("channelCfg", "channelCfg 15 8 0"), match: "TX mask"},
		{name: "invalid TX mask", mutate: replace("channelCfg", "channelCfg 15 tx 0"), match: "invalid channelCfg TX mask"},
		{name: "cascaded channel config", mutate: replace("channelCfg", "channelCfg 15 7 1"), match: "single-chip"},
		{name: "malformed channel", mutate: replace("channelCfg", "channelCfg 15 7"), match: "RX mask, TX mask"},
		{name: "missing profile", mutate: removeCommand("profileCfg"), match: "profileCfg"},
		{name: "duplicate profile ID", mutate: func(commands []string) []string {
			return replaceCommandsByName(commands, "profileCfg", profile, profile)
		}, match: "defined more than once"},
		{name: "studio multiple profiles", mutate: func(commands []string) []string {
			return replaceCommandsByName(
				commands,
				"profileCfg",
				profile,
				"profileCfg 1 60 7 3 24 0 0 166 1 128 12500 0 0 158",
			)
		}, match: "exactly one profileCfg"},
		{name: "studio profile must be zero", mutate: func(commands []string) []string {
			commands = replaceCommandsByName(commands, "profileCfg", "profileCfg 1 60 7 3 24 0 0 166 1 256 12500 0 0 158")
			return replaceCommandsByName(
				commands,
				"chirpCfg",
				"chirpCfg 0 0 1 0 0 0 0 1",
				"chirpCfg 1 1 1 0 0 0 0 4",
			)
		}, match: "profile ID 0"},
		{name: "profile ID outside xWR68xx range", mutate: replace("profileCfg", "profileCfg 4 60 7 3 24 0 0 166 1 256 12500 0 0 158"), match: "0..3"},
		{name: "start frequency outside xWR68xx band", mutate: replace("profileCfg", "profileCfg 0 77 7 3 24 0 0 166 1 256 12500 0 0 158"), match: "outside 57..64 GHz"},
		{name: "studio negative frequency slope", mutate: replace("profileCfg", "profileCfg 0 60 7 3 24 0 0 -166 1 256 12500 0 0 158"), match: "negative profileCfg frequency slope"},
		{name: "zero ADC samples", mutate: replace("profileCfg", "profileCfg 0 60 7 3 24 0 0 166 1 0 12500 0 0 158"), match: "must be positive"},
		{name: "ADC sample integer overflow", mutate: replace("profileCfg", "profileCfg 0 60 7 3 24 0 0 166 1 65536 12500 0 0 158"), match: "invalid profileCfg numAdcSamples"},
		{name: "malformed profile", mutate: replace("profileCfg", "profileCfg 0 60"), match: "fourteen arguments"},
		{name: "unknown chirp profile", mutate: replace("chirpCfg", "chirpCfg 0 0 1 0 0 0 0 1"), match: "without a matching profileCfg"},
		{name: "missing chirp", mutate: removeCommand("chirpCfg"), match: "chirpCfg"},
		{name: "malformed chirp", mutate: replace("chirpCfg", "chirpCfg 0 0 0"), match: "eight arguments"},
		{name: "reversed chirp range", mutate: replace("chirpCfg", "chirpCfg 1 0 0 0 0 0 0 1"), match: "before its start"},
		{name: "chirp index outside xWR68xx range", mutate: replace("chirpCfg", "chirpCfg 0 512 0 0 0 0 0 1"), match: "0..511"},
		{name: "chirp index integer overflow", mutate: replace("chirpCfg", "chirpCfg 0 65536 0 0 0 0 0 1"), match: "invalid chirpCfg end index"},
		{name: "chirp profile outside xWR68xx range", mutate: replace("chirpCfg", "chirpCfg 0 0 4 0 0 0 0 1"), match: "chirpCfg profile ID must be in 0..3"},
		{name: "chirp TX mask outside xWR68xx range", mutate: replace("chirpCfg", "chirpCfg 0 0 0 0 0 0 0 8"), match: "TX0..TX2"},
		{name: "chirp TX mask integer overflow", mutate: replace("chirpCfg", "chirpCfg 0 0 0 0 0 0 0 65536"), match: "invalid chirpCfg TX enable mask"},
		{name: "chirp TX not enabled by channel", mutate: func(commands []string) []string {
			return replaceCommand(commands, "channelCfg", "channelCfg 15 3 0")
		}, match: "subset of channelCfg TX mask"},
		{name: "three simultaneous chirp transmitters", mutate: replace("chirpCfg", "chirpCfg 0 0 0 0 0 0 0 7"), match: "at most two transmitters"},
		{name: "frame has no enabled transmitter", mutate: func(commands []string) []string {
			return replaceCommandsByName(
				commands,
				"chirpCfg",
				"chirpCfg 0 0 0 0 0 0 0 0",
				"chirpCfg 1 1 0 0 0 0 0 0",
			)
		}, match: "at least one channelCfg transmitter"},
		{name: "overlapping chirp ranges", mutate: func(commands []string) []string {
			return replaceCommandsByName(
				commands,
				"chirpCfg",
				"chirpCfg 0 1 0 0 0 0 0 1",
				"chirpCfg 1 1 0 0 0 0 0 4",
			)
		}, match: "ambiguous"},
		{name: "frame chirp gap", mutate: func(commands []string) []string {
			return replaceCommandsByName(commands, "chirpCfg", "chirpCfg 0 0 0 0 0 0 0 1")
		}, match: "has no chirpCfg-to-profile mapping"},
		{name: "studio unique chirp limit", mutate: func(commands []string) []string {
			commands = replaceCommandsByName(commands, "chirpCfg", "chirpCfg 0 32 0 0 0 0 0 1")
			return replaceCommand(commands, "frameCfg", "frameCfg 0 32 1 1 100 1 0")
		}, match: "at most 32 unique frame chirps"},
		{name: "studio chirpCfg storage limit", mutate: func(commands []string) []string {
			commands = replaceCommandsByName(
				commands,
				"chirpCfg",
				"chirpCfg 0 0 0 0 0 0 0 1",
				"chirpCfg 1 1 0 0 0 0 0 1",
				"chirpCfg 2 2 0 0 0 0 0 1",
				"chirpCfg 3 3 0 0 0 0 0 1",
				"chirpCfg 4 4 0 0 0 0 0 1",
				"chirpCfg 5 5 0 0 0 0 0 1",
			)
			return replaceCommand(commands, "frameCfg", "frameCfg 0 5 1 1 100 1 0")
		}, match: "at most five chirpCfg ranges"},
		{name: "reversed frame range", mutate: replace("frameCfg", "frameCfg 1 0 32 100 100 1 0"), match: "before its start"},
		{name: "frame chirp outside xWR68xx range", mutate: replace("frameCfg", "frameCfg 0 512 1 100 100 1 0"), match: "0..511"},
		{name: "zero frame loops", mutate: replace("frameCfg", "frameCfg 0 1 0 100 100 1 0"), match: "1..255"},
		{name: "too many frame loops", mutate: replace("frameCfg", "frameCfg 0 1 256 100 100 1 0"), match: "1..255"},
		{name: "frame loop integer overflow", mutate: replace("frameCfg", "frameCfg 0 1 65536 100 100 1 0"), match: "invalid frame loop count"},
		{name: "frame count integer overflow", mutate: replace("frameCfg", "frameCfg 0 1 32 65536 100 1 0"), match: "invalid frame count"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := test.mutate(append([]string(nil), validCommands()...))
			_, err := CommandPlan(commands)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.match)) {
				t.Fatalf("error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func TestExpectedBytesSupportsMaximumHardwareFeasibleSamples(t *testing.T) {
	commands := replaceCommand(
		validCommands(),
		"profileCfg",
		"profileCfg 0 60 7 3 24 0 0 166 1 2048 12500 0 0 158",
	)
	commands = replaceCommandsByName(commands, "chirpCfg", "chirpCfg 0 31 0 0 0 0 0 1")
	commands = replaceCommand(commands, "frameCfg", "frameCfg 0 31 255 65535 100 1 0")

	plan, err := CommandPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	const want int64 = 4 * 4 * 2048 * 32 * 255 * 65535
	if plan.ExpectedBytes != want {
		t.Fatalf("ExpectedBytes = %d, want %d", plan.ExpectedBytes, want)
	}
}

func TestRawBufferHardwareBounds(t *testing.T) {
	profile := func(samples int) string {
		return fmt.Sprintf("profileCfg 0 60 7 3 24 0 0 166 1 %d 12500 0 0 158", samples)
	}
	tests := []struct {
		name      string
		rxMask    int
		samples   int
		wantError string
	}{
		{name: "ADCBuf exact capacity", rxMask: 15, samples: 2048},
		{name: "ADCBuf aligned overflow", rxMask: 15, samples: 2049, wantError: "ADCBuf"},
		{name: "CBUFF exact minimum", rxMask: 1, samples: 16},
		{name: "CBUFF below minimum", rxMask: 1, samples: 15, wantError: "at least 64 bytes"},
		{name: "CBUFF exact linked-list maximum", rxMask: 1, samples: 8191},
		{name: "CBUFF linked-list overflow", rxMask: 1, samples: 8192, wantError: "linked-list transfer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := replaceCommand(validCommands(), "profileCfg", profile(test.samples))
			commands = replaceCommand(commands, "channelCfg", fmt.Sprintf("channelCfg %d 7 0", test.rxMask))
			_, err := CommandPlan(commands)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestExpectedByteArithmeticBoundaries(t *testing.T) {
	maximum := ^uint64(0)
	if got, err := checkedExpectedMultiply(maximum/3, 3, "test"); err != nil || got != maximum-maximum%3 {
		t.Fatalf("checkedExpectedMultiply boundary = %d, %v", got, err)
	}
	if _, err := checkedExpectedMultiply(maximum/3+1, 3, "test"); err == nil {
		t.Fatal("checkedExpectedMultiply accepted uint64 overflow")
	}
	if got, err := checkedExpectedAdd(maximum-1, 1, "test"); err != nil || got != maximum {
		t.Fatalf("checkedExpectedAdd boundary = %d, %v", got, err)
	}
	if _, err := checkedExpectedAdd(maximum, 1, "test"); err == nil {
		t.Fatal("checkedExpectedAdd accepted uint64 overflow")
	}
	if got, err := checkedExpectedInt64(uint64(math.MaxInt64), "test"); err != nil || got != math.MaxInt64 {
		t.Fatalf("checkedExpectedInt64 boundary = %d, %v", got, err)
	}
	if _, err := checkedExpectedInt64(uint64(math.MaxInt64)+1, "test"); err == nil {
		t.Fatal("checkedExpectedInt64 accepted int64 overflow")
	}
}

func TestMaxDurationRejectsGuardOverflow(t *testing.T) {
	plan := Plan{
		NumberOfFrames: 1,
		FramePeriod:    time.Duration(math.MaxInt64),
	}
	if _, err := plan.MaxDuration(time.Second); err == nil {
		t.Fatal("MaxDuration accepted an overflow")
	}
}

func TestPlanRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]string) []string
		match  string
	}{
		{name: "missing mode", mutate: removeCommand("dfeDataOutputMode"), match: "dfeDataOutputMode"},
		{name: "advanced mode", mutate: replace("dfeDataOutputMode", "dfeDataOutputMode 3"), match: "legacy"},
		{name: "mode case", mutate: replace("dfeDataOutputMode", "dfedataoutputmode 1"), match: "case-sensitive"},
		{name: "missing adc", mutate: removeCommand("adcCfg"), match: "adcCfg"},
		{name: "12-bit adc", mutate: replace("adcCfg", "adcCfg 0 1"), match: "16-bit complex"},
		{name: "real adc", mutate: replace("adcCfg", "adcCfg 2 0"), match: "16-bit complex"},
		{name: "missing ADCBuf", mutate: removeCommand("adcbufCfg"), match: "exactly one adcbufCfg"},
		{name: "duplicate ADCBuf", mutate: func(commands []string) []string {
			return replaceCommandsByName(commands, "adcbufCfg", "adcbufCfg -1 0 1 1 1", "adcbufCfg -1 0 1 1 1")
		}, match: "exactly one adcbufCfg"},
		{name: "malformed ADCBuf", mutate: replace("adcbufCfg", "adcbufCfg -1 0 1"), match: "must contain subframe"},
		{name: "invalid ADCBuf integer", mutate: replace("adcbufCfg", "adcbufCfg -1 complex 1 1 1"), match: "cannot parse adcbufCfg"},
		{name: "real ADCBuf", mutate: replace("adcbufCfg", "adcbufCfg -1 1 1 1 1"), match: "complex ADCBuf format"},
		{name: "ADCBuf multi-chirp threshold", mutate: replace("adcbufCfg", "adcbufCfg -1 0 1 1 2"), match: "chirpThreshold=1"},
		{name: "studio ADCBuf subframe", mutate: replace("adcbufCfg", "adcbufCfg 0 0 1 1 1"), match: "requires adcbufCfg -1 0 1 1 1"},
		{name: "studio ADCBuf IQ swap", mutate: replace("adcbufCfg", "adcbufCfg -1 0 0 1 1"), match: "requires adcbufCfg -1 0 1 1 1"},
		{name: "studio ADCBuf interleave", mutate: replace("adcbufCfg", "adcbufCfg -1 0 1 0 1"), match: "requires adcbufCfg -1 0 1 1 1"},
		{name: "missing LVDS", mutate: removeCommand("lvdsStreamCfg"), match: "lvdsStreamCfg"},
		{name: "LVDS header", mutate: replace("lvdsStreamCfg", "lvdsStreamCfg -1 1 1 0"), match: "no header"},
		{name: "software LVDS", mutate: replace("lvdsStreamCfg", "lvdsStreamCfg -1 0 1 1"), match: "software off"},
		{name: "missing frame", mutate: removeCommand("frameCfg"), match: "exactly one"},
		{name: "duplicate frame", mutate: func(commands []string) []string {
			index := len(commands) - 1
			return append(commands[:index], append([]string{"frameCfg 0 1 32 100 100 1 0"}, commands[index:]...)...)
		}, match: "exactly one"},
		{name: "hardware trigger", mutate: replace("frameCfg", "frameCfg 0 1 32 100 100 2 0"), match: "software-triggered"},
		{name: "zero period", mutate: replace("frameCfg", "frameCfg 0 1 32 100 0 1 0"), match: "periodicity"},
		{name: "period below xWR68xx minimum", mutate: replace("frameCfg", "frameCfg 0 1 32 100 0.299 1 0"), match: "0.3..1342"},
		{name: "period above xWR68xx maximum", mutate: replace("frameCfg", "frameCfg 0 1 32 100 1342.001 1 0"), match: "0.3..1342"},
		{name: "nonzero frame trigger delay", mutate: replace("frameCfg", "frameCfg 0 1 32 100 100 1 0.1"), match: "frameTriggerDelay=0"},
		{name: "mode after profile", mutate: func(commands []string) []string {
			modeIndex, profileIndex := -1, -1
			for index, command := range commands {
				if isCommand(command, "dfeDataOutputMode") {
					modeIndex = index
				}
				if isCommand(command, "profileCfg") {
					profileIndex = index
				}
			}
			commands[modeIndex], commands[profileIndex] = commands[profileIndex], commands[modeIndex]
			return commands
		}, match: "must precede"},
		{name: "embedded stop", mutate: func(commands []string) []string {
			return append(commands[:len(commands)-1], "sensorStop", commands[len(commands)-1])
		}, match: "capture coordinator"},
		{name: "start arguments", mutate: replace("sensorStart", "sensorStart 1"), match: "exact sensorStart"},
		{name: "start case", mutate: replace("sensorStart", "SensorStart"), match: "case-sensitive"},
		{name: "start not last", mutate: func(commands []string) []string {
			return append(commands, "lowPower 0 0")
		}, match: "final"},
		{name: "duplicate start", mutate: appendCommand("sensorStart"), match: "only one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := test.mutate(append([]string(nil), validCommands()...))
			_, err := CommandPlan(commands)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.match)) {
				t.Fatalf("error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func replaceCommand(commands []string, name, replacement string) []string {
	result := append([]string(nil), commands...)
	for index, command := range result {
		if isCommand(command, name) {
			result[index] = replacement
			return result
		}
	}
	return result
}

func replaceCommandsByName(commands []string, name string, replacements ...string) []string {
	result := make([]string, 0, len(commands)+len(replacements))
	replaced := false
	for _, command := range commands {
		if !isCommand(command, name) {
			result = append(result, command)
			continue
		}
		if !replaced {
			result = append(result, replacements...)
			replaced = true
		}
	}
	return result
}

func replace(name, replacement string) func([]string) []string {
	return func(commands []string) []string {
		return replaceCommand(commands, name, replacement)
	}
}

func removeCommand(name string) func([]string) []string {
	return func(commands []string) []string {
		result := make([]string, 0, len(commands))
		for _, command := range commands {
			if !isCommand(command, name) {
				result = append(result, command)
			}
		}
		return result
	}
}

func appendCommand(command string) func([]string) []string {
	return func(commands []string) []string { return append(commands, command) }
}

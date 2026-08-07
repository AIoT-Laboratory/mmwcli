package radar

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDialectBaudRatesAndPlatformGates(t *testing.T) {
	if SDKDemo.Name() != "demo" || SDKDemo.DefaultBaud() != 115200 {
		t.Fatalf("SDKDemo = %q/%d", SDKDemo.Name(), SDKDemo.DefaultBaud())
	}
	if StudioCLI.Name() != "studio-cli" || StudioCLI.DefaultBaud() != 921600 {
		t.Fatalf("StudioCLI = %q/%d", StudioCLI.Name(), StudioCLI.DefaultBaud())
	}
	if !SDKDemo.RequiresPlatformVerification() || !StudioCLI.RequiresPlatformVerification() {
		t.Fatal("text CLI dialect does not require platform verification")
	}
	if SDKDemo.DeviceFamily() != xwr68xxFamily || StudioCLI.DeviceFamily() != xwr68xxFamily {
		t.Fatal("default demo and studio_cli dialects must remain bound to xwr68xx")
	}
}

func TestDeviceFamilyDescriptors(t *testing.T) {
	tests := []struct {
		name      string
		family    DeviceFamily
		platforms [2]string
		rxMask    uint64
		txMask    uint64
		minimum   float64
		maximum   float64
	}{
		{name: "xwr16xx", family: xwr16xxFamily, platforms: [2]string{"xWR16xx"}, rxMask: 0x0f, txMask: 0x03, minimum: 76, maximum: 81},
		{name: "xwr18xx", family: xwr18xxFamily, platforms: [2]string{"xWR18xx"}, rxMask: 0x0f, txMask: 0x07, minimum: 76, maximum: 81},
		{name: "xwr64xx", family: xwr64xxFamily, platforms: [2]string{"xWR64xx"}, rxMask: 0x0f, txMask: 0x07, minimum: 57, maximum: 64},
		{name: "xwr68xx", family: xwr68xxFamily, platforms: [2]string{"xWR68xx"}, rxMask: 0x0f, txMask: 0x07, minimum: 57, maximum: 64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			family, err := ParseDeviceFamily(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if family != test.family || family.Name() != test.name {
				t.Fatalf("ParseDeviceFamily(%q) = %+v", test.name, family)
			}
			if family.versionPlatforms != test.platforms ||
				family.receiverMask != test.rxMask || family.transmitterMask != test.txMask ||
				family.minimumStartFrequencyGHz != test.minimum || family.maximumStartFrequencyGHz != test.maximum ||
				family.adcBufBytes != 32*1024 || family.lvdsLaneCount != 2 {
				t.Fatalf("descriptor = %+v", family)
			}

			dialect, err := SDKDemoForFamily(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if dialect.Name() != "demo" || dialect.DefaultBaud() != 115200 ||
				!dialect.RequiresPlatformVerification() || dialect.DeviceFamily() != family {
				t.Fatalf("SDKDemoForFamily(%q) = %+v", test.name, dialect)
			}
		})
	}

	for _, name := range []string{"", "XWR16XX", " xwr16xx", "xwr16xx ", "xwr14xx", "xWR18xx_AOP"} {
		if _, err := ParseDeviceFamily(name); err == nil {
			t.Errorf("ParseDeviceFamily(%q) accepted a non-canonical name", name)
		}
		if _, err := SDKDemoForFamily(name); err == nil {
			t.Errorf("SDKDemoForFamily(%q) accepted a non-canonical name", name)
		}
	}
}

func TestFindTerminalIsStrict(t *testing.T) {
	tests := []struct {
		name     string
		response string
		status   TerminalStatus
		code     int
	}{
		{name: "done", response: "echo\r\nDone\r\n", status: TerminalDone},
		{name: "numeric error", response: "Error -54\n", status: TerminalError, code: -54},
		{name: "diagnostic error", response: "Error: mailbox unavailable\n", status: TerminalPending},
		{name: "bare error", response: "Error\n", status: TerminalPending},
		{name: "tab error", response: "Error\t-54\n", status: TerminalPending},
		{name: "error suffix", response: "Error -54 more\n", status: TerminalPending},
		{name: "wrong done case", response: "done\n", status: TerminalPending},
		{name: "done substring", response: "NotDone\n", status: TerminalPending},
		{name: "diagnostic before done", response: "Error: diagnostic only\nDone\n", status: TerminalDone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := FindTerminal(test.response)
			if result.Status != test.status || result.ErrorCode != test.code {
				t.Fatalf("FindTerminal(%q) = %+v", test.response, result)
			}
		})
	}
}

func TestStudioErrorDescriptions(t *testing.T) {
	want := map[int]string{
		-50: "invalid command",
		-51: "invalid command usage",
		-52: "invalid input parameter",
		-53: "sensorStart reconfiguration argument must be omitted or zero",
		-54: "sensor is already in the requested start/stop state",
		-55: "LVDS software or header streaming is unsupported",
		-56: "command timed out",
		-57: "data-path configuration failed",
		-58: "configuration command is not allowed after sensorStart",
	}
	for code, expected := range want {
		actual, ok := StudioErrorDescription(code)
		if !ok || actual != expected {
			t.Errorf("StudioErrorDescription(%d) = %q/%v", code, actual, ok)
		}
	}
	if _, ok := StudioErrorDescription(-59); ok {
		t.Fatal("unknown error code was reported as known")
	}
}

func TestAlreadyStoppedIsNarrow(t *testing.T) {
	stop := fmt.Errorf("wrapped: %w", &CommandError{Command: "sensorStop", Code: -54})
	if !IsAlreadyStopped(stop) {
		t.Fatal("wrapped sensorStop Error -54 was not idempotent")
	}
	if IsAlreadyStopped(&CommandError{Command: "sensorStart", Code: -54}) {
		t.Fatal("sensorStart Error -54 was incorrectly idempotent")
	}
	if IsAlreadyStopped(&CommandError{Command: "sensorStop", Code: -56}) {
		t.Fatal("sensorStop Error -56 was incorrectly idempotent")
	}
	if IsAlreadyStopped(errors.New("Error -54")) {
		t.Fatal("untyped message was incorrectly idempotent")
	}
}

func TestVerifyPlatformResponse(t *testing.T) {
	for _, dialect := range []Dialect{SDKDemo, StudioCLI} {
		for _, response := range []string{
			"Platform                : xWR68xx\r\nDone\r\n",
			"Platform:xwr68XX\nDone\n",
			"Banner\nPlatform : xWR68xx\n",
		} {
			if err := dialect.VerifyPlatformResponse(response); err != nil {
				t.Errorf("%s valid response rejected: %v", dialect.Name(), err)
			}
		}
		for _, response := range []string{
			"Platform : xWR18xx\nDone\n",
			"PlatformName : xWR68xx\nDone\n",
			"Device Info : xWR6843\nDone\n",
		} {
			if err := dialect.VerifyPlatformResponse(response); err == nil {
				t.Errorf("%s invalid response accepted: %q", dialect.Name(), response)
			}
		}
	}
}

func TestSDKDemoFamilyPlatformResponses(t *testing.T) {
	tests := []struct {
		family   string
		accepted []string
		rejected []string
	}{
		{family: "xwr16xx", accepted: []string{"xWR16xx"}, rejected: []string{"xWR18xx", "xWR68xx"}},
		{family: "xwr18xx", accepted: []string{"xWR18xx"}, rejected: []string{"xWR18xx_AOP", "xWR16xx", "xWR68xx"}},
		{family: "xwr64xx", accepted: []string{"xWR64xx"}, rejected: []string{"xWR68xx", "xWR18xx"}},
		{family: "xwr68xx", accepted: []string{"xWR68xx"}, rejected: []string{"xWR68xx_AOP", "xWR64xx"}},
	}
	for _, test := range tests {
		t.Run(test.family, func(t *testing.T) {
			dialect, err := SDKDemoForFamily(test.family)
			if err != nil {
				t.Fatal(err)
			}
			for _, platform := range test.accepted {
				response := "Platform : " + platform + "\nDone\n"
				if err := dialect.VerifyPlatformResponse(response); err != nil {
					t.Errorf("accepted platform %q rejected: %v", platform, err)
				}
			}
			for _, platform := range test.rejected {
				response := "Platform : " + platform + "\nDone\n"
				if err := dialect.VerifyPlatformResponse(response); err == nil {
					t.Errorf("unsupported platform %q accepted", platform)
				}
			}
		})
	}
}

func TestStudioRawOnlyAllowlist(t *testing.T) {
	commands := validCommands()
	if err := StudioCLI.ValidateConfiguration(commands, true); err != nil {
		t.Fatalf("valid raw-only commands rejected: %v", err)
	}
	if err := SDKDemo.ValidateConfiguration([]string{"unextendedDemoCommand 1"}, true); err != nil {
		t.Fatalf("SDK demo command was subjected to studio allowlist: %v", err)
	}
}

func TestStudioRawOnlyRejectsUnsafeCommands(t *testing.T) {
	tests := []struct {
		name    string
		command string
		match   string
	}{
		{name: "monitor", command: "calibMonCfg 1 1", match: "monitor"},
		{name: "reset", command: "deviceRestart", match: "reset"},
		{name: "advanced", command: "advFrameCfg 1", match: "advanced"},
		{name: "continuous", command: "contModeCfg 1", match: "continuous"},
		{name: "test", command: "testSrcCfg 1", match: "test"},
		{name: "loopback", command: "loopBackCfg 1", match: "loopback"},
		{name: "embedded stop", command: "sensorStop", match: "allowlist"},
		{name: "unknown", command: "futureCommand 1", match: "allowlist"},
		{name: "wrong case", command: "ProfileCfg 1", match: "case-sensitive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := append([]string(nil), validCommands()...)
			commands = append(commands[:1], append([]string{test.command}, commands[1:]...)...)
			err := StudioCLI.ValidateConfiguration(commands, true)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.match)) {
				t.Fatalf("ValidateConfiguration error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestStudioFlushRules(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
		full     bool
	}{
		{name: "missing full flush", commands: validCommands()[1:], full: true},
		{name: "flush not first", commands: []string{"channelCfg 15 7 0", "flushCfg"}, full: true},
		{name: "duplicate flush", commands: []string{"flushCfg", "flushCfg"}, full: true},
		{name: "flush arguments", commands: []string{"flushCfg 1"}, full: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := StudioCLI.ValidateConfiguration(test.commands, test.full); err == nil {
				t.Fatal("invalid flush configuration accepted")
			}
		})
	}
	if err := StudioCLI.ValidateConfiguration(validCommands()[1:], false); err != nil {
		t.Fatalf("reuse validation required flushCfg: %v", err)
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

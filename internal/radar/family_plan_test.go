package radar

import (
	"strconv"
	"strings"
	"testing"
)

func TestDeviceFamiliesAreClosedExactLegacyCaptureDescriptors(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		txMask      uint64
		minimumGHz  float64
		maximumGHz  float64
		rawContract RawCaptureContract
	}{
		{name: "xwr16xx", platform: "xWR16xx", txMask: 0x03, minimumGHz: 76, maximumGHz: 81, rawContract: xwr16xxRawCapture},
		{name: "xwr18xx", platform: "xWR18xx", txMask: 0x07, minimumGHz: 76, maximumGHz: 81, rawContract: xwr18xxRawCapture},
		{name: "xwr68xx", platform: "xWR68xx", txMask: 0x07, minimumGHz: 57, maximumGHz: 64, rawContract: xwr68xxRawCapture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			family, err := ParseDeviceFamily(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if !family.Valid() || family.Name() != test.name ||
				family.versionPlatforms != [2]string{test.platform} ||
				family.receiverMask != 0x0f || family.transmitterMask != test.txMask ||
				family.minimumStartFrequencyGHz != test.minimumGHz ||
				family.maximumStartFrequencyGHz != test.maximumGHz ||
				family.adcBufBytes != 32*1024 || family.lvdsLaneCount != 2 {
				t.Fatalf("family = %+v", family)
			}
			contract := family.RawCaptureContract()
			if !contract.Valid() || contract != test.rawContract ||
				contract.Vendor() != "ti" || contract.Family() != test.name ||
				contract.Model() != "" || contract.Revision() != "" ||
				contract.IdentitySource() != "route_declaration" ||
				contract.ConfigFormat() != "ti_mmwave_legacy_cli.v1" ||
				contract.DataType() != "int16" || contract.ByteOrder() != "little" ||
				contract.LaneCount() != 2 || contract.Layout() != "group2_i_then_q" {
				t.Fatalf("raw capture contract = %+v", contract)
			}
		})
	}

	for _, invalid := range []string{"", "XWR16XX", "xWR18xx", "xwr1642", "iwr6843", "xwr68xx_aop", "default", " xwr68xx"} {
		if family, err := ParseDeviceFamily(invalid); err == nil || family.Valid() {
			t.Fatalf("ParseDeviceFamily(%q) = %+v, %v", invalid, family, err)
		}
	}
	mutated := xwr16xxFamily
	mutated.transmitterMask = 0x07
	if mutated.Valid() || mutated.RawCaptureContract().Valid() {
		t.Fatal("mutated family descriptor was accepted")
	}
}

func TestBuildCapturePlansForExplicitFamilies(t *testing.T) {
	for _, name := range []string{"xwr16xx", "xwr18xx", "xwr68xx"} {
		t.Run(name, func(t *testing.T) {
			family, err := ParseDeviceFamily(name)
			if err != nil {
				t.Fatal(err)
			}
			commands := validFamilyCommands(name, 256)
			plan, err := BuildCapturePlanForFamily(family, commands)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Mode != FullConfiguration || plan.Dialect != StudioCLI ||
				plan.DeviceFamily() != family || plan.RawCapture != family.RawCaptureContract() ||
				plan.BytesPerFrame != 262_144 || plan.ExpectedBytes != 26_214_400 {
				t.Fatalf("plan = %+v", plan)
			}

			snapshot := renderSessionConfig(commands)
			sessionPlan, err := BuildCaptureSessionV1PlanForFamily(family, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !sameCapturePlan(sessionPlan, plan) {
				t.Fatalf("session plan differs from family plan:\n%+v\n%+v", sessionPlan, plan)
			}
			if err := ValidateCaptureSessionV1Plan(snapshot, sessionPlan); err != nil {
				t.Fatalf("validate session plan: %v", err)
			}
		})
	}

	if _, err := BuildCapturePlanForFamily(DeviceFamily{}, validCommands()); err == nil {
		t.Fatal("family plan builder accepted zero family")
	}
	if _, err := BuildCaptureSessionV1PlanForFamily(DeviceFamily{}, renderSessionConfig(validCommands())); err == nil {
		t.Fatal("family session builder accepted zero family")
	}
}

func TestFamilyCaptureBandsAndTransmitterMasks(t *testing.T) {
	tests := []struct {
		name             string
		validFrequencies []string
		invalidFrequency string
		invalidTXMask    string
	}{
		{name: "xwr16xx", validFrequencies: []string{"76", "77", "81"}, invalidFrequency: "60", invalidTXMask: "4"},
		{name: "xwr18xx", validFrequencies: []string{"76", "77", "81"}, invalidFrequency: "60", invalidTXMask: "8"},
		{name: "xwr68xx", validFrequencies: []string{"57", "60", "64"}, invalidFrequency: "77", invalidTXMask: "8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			family, err := ParseDeviceFamily(test.name)
			if err != nil {
				t.Fatal(err)
			}
			for _, frequency := range test.validFrequencies {
				commands := replaceFamilyProfileFrequency(validFamilyCommands(test.name, 256), frequency)
				if _, err := BuildCapturePlanForFamily(family, commands); err != nil {
					t.Errorf("frequency %s rejected: %v", frequency, err)
				}
			}

			commands := replaceFamilyProfileFrequency(validFamilyCommands(test.name, 256), test.invalidFrequency)
			if _, err := BuildCapturePlanForFamily(family, commands); err == nil || !strings.Contains(err.Error(), "outside") {
				t.Fatalf("invalid frequency error = %v", err)
			}
			commands = replaceCommand(
				validFamilyCommands(test.name, 256),
				"channelCfg",
				"channelCfg 15 "+test.invalidTXMask+" 0",
			)
			if _, err := BuildCapturePlanForFamily(family, commands); err == nil || !strings.Contains(err.Error(), "TX mask") {
				t.Fatalf("invalid TX error = %v", err)
			}
		})
	}
}

func TestXWR16And18CaptureEnforce32KiBADCBuf(t *testing.T) {
	for _, name := range []string{"xwr16xx", "xwr18xx"} {
		t.Run(name, func(t *testing.T) {
			family, err := ParseDeviceFamily(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := BuildCapturePlanForFamily(family, validFamilyCommands(name, 2048)); err != nil {
				t.Fatalf("exact 32 KiB ADCBuf rejected: %v", err)
			}
			if _, err := BuildCapturePlanForFamily(family, validFamilyCommands(name, 2049)); err == nil ||
				!strings.Contains(err.Error(), "ADCBuf") {
				t.Fatalf("ADCBuf overflow error = %v", err)
			}
		})
	}
}

func TestStudioTransportRejectsExplicitNonXWR68FamilyPlanBeforeIO(t *testing.T) {
	plan, err := BuildCapturePlanForFamily(xwr18xxFamily, validFamilyCommands("xwr18xx", 256))
	if err != nil {
		t.Fatal(err)
	}
	client, transport := newScriptedClient(t, StudioCLI)
	if err := client.Apply(plan); err == nil || !strings.Contains(err.Error(), "family") {
		t.Fatalf("Studio transport family error = %v", err)
	}
	if len(transport.commands) != 0 {
		t.Fatalf("Studio transport received commands: %v", transport.commands)
	}
}

func validFamilyCommands(name string, samples uint64) []string {
	commands := validCommands()
	frequency := "60"
	if name == "xwr16xx" || name == "xwr18xx" {
		frequency = "77"
	}
	commands = replaceFamilyProfileFrequency(commands, frequency)
	commands = replaceCommand(
		commands,
		"profileCfg",
		strings.Replace(
			commandByName(commands, "profileCfg"),
			" 256 12500 ",
			" "+strconv.FormatUint(samples, 10)+" 12500 ",
			1,
		),
	)
	if name == "xwr16xx" {
		commands = replaceCommand(commands, "channelCfg", "channelCfg 15 3 0")
		commands = replaceCommandsByName(
			commands,
			"chirpCfg",
			"chirpCfg 0 0 0 0 0 0 0 1",
			"chirpCfg 1 1 0 0 0 0 0 2",
		)
	}
	return commands
}

func replaceFamilyProfileFrequency(commands []string, frequency string) []string {
	profile := strings.Fields(commandByName(commands, "profileCfg"))
	profile[2] = frequency
	return replaceCommand(commands, "profileCfg", strings.Join(profile, " "))
}

func commandByName(commands []string, name string) string {
	for _, command := range commands {
		if isCommand(command, name) {
			return command
		}
	}
	return ""
}

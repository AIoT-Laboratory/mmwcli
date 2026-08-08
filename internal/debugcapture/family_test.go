package debugcapture

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

func TestIWR6843ES2DebugFamilyContractIsClosed(t *testing.T) {
	family, err := debugFamilyContractForID(debugFamilyIWR6843ES2)
	if err != nil {
		t.Fatalf("debugFamilyContractForID: %v", err)
	}
	if !family.valid() {
		t.Fatal("canonical IWR6843 ES2 debug family is invalid")
	}
	if family.identity != "IWR6843 ES2" ||
		family.name != "xwr68xx" ||
		family.platform != "xWR68xx" ||
		family.imagePolicy != debugFirmwareImageIWR6843RPRC ||
		family.partCount != 1 || family.partNumbers[0] != iwr68xxES2PartNumber ||
		family.rfPolicy != debugRFEncodingIWR6843ES2 ||
		family.txMask != 0x07 ||
		family.maxChirpTransmitters != 2 ||
		family.lowPowerADCMode != 0 ||
		family.capturePolicy != debugCaptureTwoLaneDCAType2 ||
		family.runtime.policy != debugRuntimeExact ||
		family.runtime.mss != (mmWaveLinkFirmwareRelease{Major: 2, Minor: 0, Build: 0, Debug: 3}) ||
		family.runtime.rf != (mmWaveLinkFirmwareRelease{Major: 6, Minor: 2, Build: 1, Debug: 5}) ||
		family.bootPolicy != debugBootXWR68xxRFEval {
		t.Fatalf("unexpected IWR6843 ES2 debug family: %+v", family)
	}

	mutated := family
	mutated.txMask = 0x03
	if mutated.valid() {
		t.Fatal("mutated debug family was accepted as canonical")
	}
	for _, id := range []debugFamilyID{3, 255} {
		if _, err := debugFamilyContractForID(id); err == nil {
			t.Fatalf("unknown debug family %d was accepted", id)
		}
	}
}

func TestIWR6843ES2DebugFamilyBindsRawCaptureLayout(t *testing.T) {
	device, err := ParseDeviceFamily("xwr68xx")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRawCaptureFPGAConfigForFamily(device, dca.DefaultFPGAConfig()); err != nil {
		t.Fatalf("ValidateRawCaptureFPGAConfigForFamily(default): %v", err)
	}
	invalid := dca.DefaultFPGAConfig()
	invalid.LVDSMode = 4
	if err := ValidateRawCaptureFPGAConfigForFamily(device, invalid); err == nil ||
		!strings.Contains(err.Error(), "xWR68xx raw capture") {
		t.Fatalf("invalid DCA layout error = %v", err)
	}

	family, err := debugFamilyContractForID(debugFamilyIWR6843ES2)
	if err != nil {
		t.Fatal(err)
	}
	laneEnable, err := family.capturePolicy.laneEnablePayload()
	if err != nil {
		t.Fatalf("laneEnablePayload: %v", err)
	}
	if !reflect.DeepEqual(laneEnable, []byte{3, 0, 0, 0}) {
		t.Fatalf("lane-enable payload = %v", laneEnable)
	}
}

func TestExperimentalDebugFamilyContractsAreClosed(t *testing.T) {
	tests := []struct {
		name        string
		id          debugFamilyID
		platform    string
		parts       []uint8
		bss         fileContract
		mss         fileContract
		txMask      uint16
		lowPower    uint16
		boot        debugBootPolicy
		runtime     debugRuntimePolicy
		imagePolicy debugFirmwareImagePolicy
	}{
		{
			name: "xwr16xx", id: debugFamilyXWR16XX, platform: "xWR16xx",
			parts:  []uint8{0x60, 0x61, 0x04, 0x62, 0x67, 0x66, 0x01, 0xc0, 0xc1},
			bss:    fileContract{role: "BSS", name: "xwr16xx_radarss.bin", size: 35728, sha256: "0B134A14D539292BB7E8E20C14676CEABAC2265D131C24F0526A21087ABCAD8C", target: rprcTargetBSS},
			mss:    fileContract{role: "MSS", name: "xwr16xx_masterss.bin", size: 52904, sha256: "B4044513BA44C3290639AD4C416DAF37E72DEB43567FC0DE0314DAF2546130DF", target: rprcTargetMSS},
			txMask: 0x03, lowPower: 1, boot: debugBootWarmRFEval,
			runtime: debugRuntimeDiagnostic, imagePolicy: debugFirmwareImageLegacyPatchRPRC,
		},
		{
			name: "xwr18xx", id: debugFamilyXWR18XX, platform: "xWR18xx",
			parts:  []uint8{0x70, 0x71, 0xd0, 0x05},
			bss:    fileContract{role: "BSS", name: "xwr18xx_radarss.bin", size: 35728, sha256: "0B134A14D539292BB7E8E20C14676CEABAC2265D131C24F0526A21087ABCAD8C", target: rprcTargetBSS},
			mss:    fileContract{role: "MSS", name: "xwr18xx_masterss.bin", size: 52904, sha256: "B4044513BA44C3290639AD4C416DAF37E72DEB43567FC0DE0314DAF2546130DF", target: rprcTargetMSS},
			txMask: 0x07, lowPower: 0, boot: debugBootWarmRFEval,
			runtime: debugRuntimeDiagnostic, imagePolicy: debugFirmwareImageLegacyPatchRPRC,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			family, err := debugFamilyContractForID(test.id)
			if err != nil {
				t.Fatal(err)
			}
			if !family.valid() || family.name != test.name || family.identity != test.platform ||
				family.platform != test.platform || family.assets.bss != test.bss || family.assets.mss != test.mss ||
				family.imagePolicy != test.imagePolicy || family.partCount != uint8(len(test.parts)) ||
				!slices.Equal(family.partNumbers[:family.partCount], test.parts) ||
				family.rfPolicy != debugRFEncodingXWR1XXX77GHz || family.txMask != test.txMask ||
				family.maxChirpTransmitters != 2 || family.lowPowerADCMode != test.lowPower ||
				family.capturePolicy != debugCaptureTwoLaneDCAType2 || family.runtime.policy != test.runtime ||
				family.runtime.mss != (mmWaveLinkFirmwareRelease{}) || family.runtime.rf != (mmWaveLinkFirmwareRelease{}) ||
				family.bootPolicy != test.boot {
				t.Fatalf("unexpected %s family contract: %+v", test.name, family)
			}
			encoding, err := family.rfPolicy.contract()
			if err != nil {
				t.Fatal(err)
			}
			if encoding.frequencyScale != 3.6 || encoding.startMinimum != 0x5471c71b ||
				encoding.startMaximum != 0x5a000000 || encoding.slopeMaximum != 2072 ||
				encoding.requireEven || encoding.powerBackoffMaximum != 20 ||
				encoding.rxGainMinimum != 24 || encoding.rxGainMaximum != 52 ||
				encoding.reservedRFGainTarget != 2 || encoding.sampleRateMaximum != 37500 ||
				encoding.complex1XSampleRateLimit != 18750 {
				t.Fatalf("unexpected %s RF encoding: %+v", test.name, encoding)
			}
			mutated := family
			mutated.partNumbers[0] = 0
			if mutated.valid() {
				t.Fatal("mutated family was accepted as canonical")
			}
		})
	}
}

func TestParseDebugDeviceFamilyRequiresExactCanonicalName(t *testing.T) {
	for _, name := range []string{"xwr16xx", "xwr18xx", "xwr68xx"} {
		device, err := ParseDeviceFamily(name)
		if err != nil || !device.Valid() || device.Name() != name {
			t.Fatalf("ParseDeviceFamily(%q) = %q, %v", name, device.Name(), err)
		}
	}
	for _, name := range []string{"", "XWR16XX", "iwr6843", "awr1843", "xwr14xx", "xwr16"} {
		if _, err := ParseDeviceFamily(name); err == nil {
			t.Fatalf("ParseDeviceFamily accepted alias %q", name)
		}
	}
	if _, err := CheckAssetsForFamily(radar.DeviceFamily{}, "missing-bss", "missing-mss"); err == nil ||
		!strings.Contains(err.Error(), "invalid debug-cli device family") {
		t.Fatalf("zero-family asset error = %v", err)
	}
}

func TestBuildPlanForExperimental77GHzFamilies(t *testing.T) {
	tests := []struct {
		name     string
		familyID debugFamilyID
		txMask   uint16
		lowPower uint16
	}{
		{name: "xwr16xx", familyID: debugFamilyXWR16XX, txMask: 0x03, lowPower: 1},
		{name: "xwr18xx", familyID: debugFamilyXWR18XX, txMask: 0x07, lowPower: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device, source := experimental77GHzCaptureSource(t, test.name)
			plan, err := BuildPlanForFamily(device, source)
			if err != nil {
				t.Fatal(err)
			}
			if plan.family != test.familyID || !plan.matchesCapturePlan(source) {
				t.Fatalf("plan family/source binding = %d/%t", plan.family, plan.matchesCapturePlan(source))
			}
			operations := plan.operationsCopy()
			if got := binary.LittleEndian.Uint16(operations[0].command.subblocks[0].data[2:4]); got != test.txMask {
				t.Fatalf("channel TX mask = %#x, want %#x", got, test.txMask)
			}
			if got := binary.LittleEndian.Uint16(operations[3].command.subblocks[0].data[2:4]); got != test.lowPower {
				t.Fatalf("low-power ADC mode = %d, want %d", got, test.lowPower)
			}
			if got := operations[8].command.subblocks[0].data; !slices.Equal(got, []byte{3, 0, 0, 0}) {
				t.Fatalf("lane-enable payload = %v", got)
			}
			profile := operations[10].command.subblocks[0].data
			if got := binary.LittleEndian.Uint32(profile[4:8]); got != 0x558e38e3 {
				t.Fatalf("77 GHz converted start = %#x, want %#x", got, uint32(0x558e38e3))
			}
			if got := int16(binary.LittleEndian.Uint16(profile[28:30])); got != 1035 {
				t.Fatalf("77 GHz converted slope = %d, want 1035", got)
			}
		})
	}
}

func TestExperimentalFamiliesRecordUnknownRuntimeVersions(t *testing.T) {
	for _, familyID := range []debugFamilyID{debugFamilyXWR16XX, debugFamilyXWR18XX} {
		frames := validBootstrapFrames()
		mssVersion := validMSSVersion()
		mssVersion[3], mssVersion[4], mssVersion[5], mssVersion[6] = 9, 8, 7, 6
		rfVersion := validRFVersion()
		rfVersion[3], rfVersion[4], rfVersion[5], rfVersion[6] = 5, 4, 3, 2
		frames[1] = clientTestInboundFrame(
			rhcpDirectionMSSToHost,
			rhcpMessageClassResponse,
			mmWaveLinkDeviceStatusGetMessageID,
			0,
			0,
			[]mmWaveLinkSubblock{{id: mmWaveLinkVersionSubblockID, data: mssVersion}},
		)
		frames[4] = clientTestInboundFrame(
			rhcpDirectionBSSToHost,
			rhcpMessageClassResponse,
			mmWaveLinkRFStatusGetMessageID,
			2,
			0,
			[]mmWaveLinkSubblock{{id: mmWaveLinkVersionSubblockID, data: rfVersion}},
		)
		transport := &fakeMMWaveLinkTransport{}
		transport.queueFrames(frames...)
		diagnostics, err := bootstrapMMWaveLinkForFamily(context.Background(), mustMMWaveLinkClient(t, transport), familyID)
		if err != nil {
			t.Fatalf("family %d diagnostic bootstrap: %v", familyID, err)
		}
		if diagnostics.MSS.release() != (mmWaveLinkFirmwareRelease{Major: 9, Minor: 8, Build: 7, Debug: 6}) ||
			diagnostics.RF.release() != (mmWaveLinkFirmwareRelease{Major: 5, Minor: 4, Build: 3, Debug: 2}) {
			t.Fatalf("family %d runtime diagnostics = %+v", familyID, diagnostics)
		}
	}
}

func TestLegacyPatchBSSRPRCPreflight(t *testing.T) {
	assets := fixtureSubmissionAssets(t)
	image, err := parseRPRC(makeRPRCFixture(
		0x80751,
		fixtureSection{address: 0x80000, data: []byte("BSS data")},
	))
	if err != nil {
		t.Fatal(err)
	}
	writes, err := planMemoryWrites(image, rprcTargetBSS)
	if err != nil {
		t.Fatal(err)
	}
	assets.BSS = File{
		Role: "BSS", EntryPoint: image.entryPoints[0], RPRCVersion: image.version,
		Sections: len(image.sections), Writes: len(writes), image: image, writePlan: writes,
	}
	assets.family = debugFamilyXWR16XX
	if _, err := preflightFirmwareSubmission(assets); err != nil {
		t.Fatalf("legacy patch preflight: %v", err)
	}
	assets.family = debugFamilyIWR6843ES2
	if _, err := preflightFirmwareSubmission(assets); err == nil || !strings.Contains(err.Error(), "unsupported patch entry") {
		t.Fatalf("xWR68xx accepted legacy patch BSS: %v", err)
	}
}

func experimental77GHzCaptureSource(t *testing.T, name string) (radar.DeviceFamily, radar.CapturePlan) {
	t.Helper()
	device, err := ParseDeviceFamily(name)
	if err != nil {
		t.Fatal(err)
	}
	commands := append([]string(nil), goldenDebugCaptureSource(t).ConfigurationCommands...)
	for index, command := range commands {
		fields := strings.Fields(command)
		switch fields[0] {
		case "channelCfg":
			if name == "xwr16xx" {
				fields[2] = "3"
			}
		case "profileCfg":
			fields[2] = "77"
			fields[8] = "50"
			fields[14] = "94"
		case "chirpCfg":
			if name == "xwr16xx" && fields[8] == "4" {
				fields[8] = "2"
			}
		case "lowPower":
			if name == "xwr16xx" {
				fields[2] = "1"
			}
		}
		commands[index] = strings.Join(fields, " ")
	}
	source, err := radar.BuildCapturePlanForFamily(device, commands)
	if err != nil {
		t.Fatalf("BuildCapturePlanForFamily(%s): %v", name, err)
	}
	return device, source
}

func TestControllerRejectsUnknownFamilyBeforeHardware(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ControllerOptions)
	}{
		{
			name: "plan",
			mutate: func(options *ControllerOptions) {
				options.Plan.family = debugFamilyID(1)
			},
		},
		{
			name: "firmware",
			mutate: func(options *ControllerOptions) {
				options.Assets.family = debugFamilyID(1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validControllerOptions(t)
			options.ResetSOP2 = true
			test.mutate(&options)
			hardwareCalls := 0
			_, err := openControllerWithBackend(context.Background(), options, controllerBackend{
				prepareSOP2: func(context.Context, D2XXSelectors) error {
					hardwareCalls++
					return errors.New("unexpected SOP2 reset")
				},
				openEnhanced: func(context.Context, string) (controllerEnhancedConnection, error) {
					hardwareCalls++
					return nil, errors.New("unexpected Enhanced COM open")
				},
				openD2XX: func(context.Context, D2XXSelectors) (controllerTransport, error) {
					hardwareCalls++
					return nil, errors.New("unexpected D2XX open")
				},
				bootstrap: func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
					hardwareCalls++
					return nil, mmWaveLinkDeviceDiagnostics{}, errors.New("unexpected bootstrap")
				},
			})
			if err == nil || !strings.Contains(err.Error(), "family") {
				t.Fatalf("family preflight error = %v", err)
			}
			if hardwareCalls != 0 {
				t.Fatalf("hardware calls = %d, want 0", hardwareCalls)
			}
		})
	}
}

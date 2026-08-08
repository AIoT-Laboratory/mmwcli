package debugcapture

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"mmwcli/internal/dca"
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
		family.platform != "xWR68xx" ||
		family.imagePolicy != debugFirmwareImageIWR6843RPRC ||
		family.partNumber != iwr68xxES2PartNumber ||
		family.rfPolicy != debugRFEncodingIWR6843ES2 ||
		family.txMask != 0x07 ||
		family.maxChirpTransmitters != 2 ||
		family.lowPowerADCMode != 0 ||
		family.capturePolicy != debugCaptureTwoLaneDCAType2 ||
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
	for _, id := range []debugFamilyID{1, 255} {
		if _, err := debugFamilyContractForID(id); err == nil {
			t.Fatalf("unknown debug family %d was accepted", id)
		}
	}
}

func TestIWR6843ES2DebugFamilyBindsRawCaptureLayout(t *testing.T) {
	if err := ValidateRawCaptureFPGAConfig(dca.DefaultFPGAConfig()); err != nil {
		t.Fatalf("ValidateRawCaptureFPGAConfig(default): %v", err)
	}
	invalid := dca.DefaultFPGAConfig()
	invalid.LVDSMode = 4
	if err := ValidateRawCaptureFPGAConfig(invalid); err == nil ||
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

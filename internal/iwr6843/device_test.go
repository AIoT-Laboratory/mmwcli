package iwr6843

import (
	"reflect"
	"testing"
)

func TestIWR6843Contract(t *testing.T) {
	if iwr6843Identity != "IWR6843 ES2" || iwr6843Platform != "xWR68xx" ||
		iwr6843PartNumber != iwr68xxES2PartNumber || iwr6843TXMask != 0x07 ||
		iwr6843MaxChirpTransmitters != 2 || iwr6843LowPowerADCMode != 0 ||
		iwr6843Runtime.mss != (mmWaveLinkFirmwareRelease{Major: 2, Minor: 0, Build: 0, Debug: 3}) ||
		iwr6843Runtime.rf != (mmWaveLinkFirmwareRelease{Major: 6, Minor: 2, Build: 1, Debug: 5}) {
		t.Fatal("unexpected IWR6843 contract")
	}
}

func TestIWR6843BindsDCAWireLayout(t *testing.T) {
	laneEnable := laneEnablePayload()
	if !reflect.DeepEqual(laneEnable, []byte{3, 0, 0, 0}) {
		t.Fatalf("lane-enable payload = %v", laneEnable)
	}
}

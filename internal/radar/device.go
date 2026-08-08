package radar

import (
	"fmt"
	"math/bits"
	"strings"
)

// DeviceFamily describes the hardware limits bound to a radar CLI dialect.
// Its fields are deliberately private so callers cannot fabricate descriptors.
type DeviceFamily struct {
	canonicalName            string
	versionPlatforms         [2]string
	receiverMask             uint64
	transmitterMask          uint64
	minimumStartFrequencyGHz float64
	maximumStartFrequencyGHz float64
	adcBufBytes              uint64
	lvdsLaneCount            uint8
	rawCapture               RawCaptureContract
}

// RawCaptureContract is the immutable hardware and raw-wire descriptor bound
// to a validated capture plan. Its fields are private so callers cannot
// fabricate identities or sample layouts outside an audited DeviceFamily.
type RawCaptureContract struct {
	vendor         string
	family         string
	model          string
	revision       string
	identitySource string
	configFormat   string
	dataType       string
	byteOrder      string
	laneCount      uint8
	layout         string
}

var xwr68xxRawCapture = RawCaptureContract{
	vendor:         "ti",
	family:         "xwr68xx",
	identitySource: "route_declaration",
	configFormat:   "ti_mmwave_legacy_cli.v1",
	dataType:       "int16",
	byteOrder:      "little",
	laneCount:      2,
	layout:         "group2_i_then_q",
}

var xwr68xxFamily = DeviceFamily{
	canonicalName:            "xwr68xx",
	versionPlatforms:         [2]string{"xWR68xx"},
	receiverMask:             0x0f,
	transmitterMask:          0x07,
	minimumStartFrequencyGHz: 57,
	maximumStartFrequencyGHz: 64,
	adcBufBytes:              32 * 1024,
	lvdsLaneCount:            xwr68xxRawCapture.laneCount,
	rawCapture:               xwr68xxRawCapture,
}

// Name returns the canonical family name.
func (f DeviceFamily) Name() string { return f.canonicalName }

// RawCaptureContract returns the closed raw-capture descriptor for the
// family. An invalid family returns the zero, invalid contract.
func (f DeviceFamily) RawCaptureContract() RawCaptureContract {
	if !f.valid() {
		return RawCaptureContract{}
	}
	return f.rawCapture
}

// Valid reports whether the contract is one of the closed audited
// descriptors produced by a DeviceFamily.
func (contract RawCaptureContract) Valid() bool {
	return contract == xwr68xxRawCapture
}

func (contract RawCaptureContract) Vendor() string         { return contract.vendor }
func (contract RawCaptureContract) Family() string         { return contract.family }
func (contract RawCaptureContract) Model() string          { return contract.model }
func (contract RawCaptureContract) Revision() string       { return contract.revision }
func (contract RawCaptureContract) IdentitySource() string { return contract.identitySource }
func (contract RawCaptureContract) ConfigFormat() string   { return contract.configFormat }
func (contract RawCaptureContract) DataType() string       { return contract.dataType }
func (contract RawCaptureContract) ByteOrder() string      { return contract.byteOrder }
func (contract RawCaptureContract) LaneCount() uint8       { return contract.laneCount }
func (contract RawCaptureContract) Layout() string         { return contract.layout }

func (f DeviceFamily) valid() bool {
	return f == xwr68xxFamily
}

func (f DeviceFamily) acceptsPlatform(platform string) bool {
	for _, allowed := range f.versionPlatforms {
		if allowed != "" && strings.EqualFold(platform, allowed) {
			return true
		}
	}
	return false
}

func (f DeviceFamily) expectedPlatforms() string {
	if f.versionPlatforms[1] == "" {
		return f.versionPlatforms[0]
	}
	return f.versionPlatforms[0] + " or " + f.versionPlatforms[1]
}

func (f DeviceFamily) receiverRange() string {
	return fmt.Sprintf("RX0..RX%d", bits.Len64(f.receiverMask)-1)
}

func (f DeviceFamily) transmitterRange() string {
	return fmt.Sprintf("TX0..TX%d", bits.Len64(f.transmitterMask)-1)
}

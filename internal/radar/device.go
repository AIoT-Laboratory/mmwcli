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
}

var xwr68xxFamily = DeviceFamily{
	canonicalName:            "xwr68xx",
	versionPlatforms:         [2]string{"xWR68xx"},
	receiverMask:             0x0f,
	transmitterMask:          0x07,
	minimumStartFrequencyGHz: 57,
	maximumStartFrequencyGHz: 64,
	adcBufBytes:              32 * 1024,
	lvdsLaneCount:            2,
}

// Name returns the canonical family name.
func (f DeviceFamily) Name() string { return f.canonicalName }

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

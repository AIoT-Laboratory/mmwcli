package radar

import (
	"fmt"
	"math/bits"
	"strings"
)

// DeviceFamily describes the hardware limits shared by one xWR device family.
// Its fields are deliberately private so callers can select only audited
// descriptors through ParseDeviceFamily.
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

var (
	xwr16xxFamily = DeviceFamily{
		canonicalName:            "xwr16xx",
		versionPlatforms:         [2]string{"xWR16xx"},
		receiverMask:             0x0f,
		transmitterMask:          0x03,
		minimumStartFrequencyGHz: 76,
		maximumStartFrequencyGHz: 81,
		adcBufBytes:              32 * 1024,
		lvdsLaneCount:            2,
	}
	xwr18xxFamily = DeviceFamily{
		canonicalName:            "xwr18xx",
		versionPlatforms:         [2]string{"xWR18xx"},
		receiverMask:             0x0f,
		transmitterMask:          0x07,
		minimumStartFrequencyGHz: 76,
		maximumStartFrequencyGHz: 81,
		adcBufBytes:              32 * 1024,
		lvdsLaneCount:            2,
	}
	xwr64xxFamily = DeviceFamily{
		canonicalName:            "xwr64xx",
		versionPlatforms:         [2]string{"xWR64xx"},
		receiverMask:             0x0f,
		transmitterMask:          0x07,
		minimumStartFrequencyGHz: 57,
		maximumStartFrequencyGHz: 64,
		adcBufBytes:              32 * 1024,
		lvdsLaneCount:            2,
	}
	xwr68xxFamily = DeviceFamily{
		canonicalName:            "xwr68xx",
		versionPlatforms:         [2]string{"xWR68xx"},
		receiverMask:             0x0f,
		transmitterMask:          0x07,
		minimumStartFrequencyGHz: 57,
		maximumStartFrequencyGHz: 64,
		adcBufBytes:              32 * 1024,
		lvdsLaneCount:            2,
	}
)

// ParseDeviceFamily accepts only canonical family names. It intentionally
// rejects aliases, surrounding whitespace, and case variants.
func ParseDeviceFamily(name string) (DeviceFamily, error) {
	switch name {
	case xwr16xxFamily.canonicalName:
		return xwr16xxFamily, nil
	case xwr18xxFamily.canonicalName:
		return xwr18xxFamily, nil
	case xwr64xxFamily.canonicalName:
		return xwr64xxFamily, nil
	case xwr68xxFamily.canonicalName:
		return xwr68xxFamily, nil
	default:
		return DeviceFamily{}, fmt.Errorf("unsupported device family %q; expected xwr16xx, xwr18xx, xwr64xx, or xwr68xx", name)
	}
}

// Name returns the canonical family name.
func (f DeviceFamily) Name() string { return f.canonicalName }

func (f DeviceFamily) valid() bool {
	switch f.canonicalName {
	case xwr16xxFamily.canonicalName:
		return f == xwr16xxFamily
	case xwr18xxFamily.canonicalName:
		return f == xwr18xxFamily
	case xwr64xxFamily.canonicalName:
		return f == xwr64xxFamily
	case xwr68xxFamily.canonicalName:
		return f == xwr68xxFamily
	default:
		return false
	}
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

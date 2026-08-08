package debugcapture

import (
	"fmt"
	"strings"

	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

const iwr68xxES2PartNumber = uint8(0xe2)

type debugFamilyID uint8

const (
	debugFamilyIWR6843ES2 debugFamilyID = iota
	debugFamilyXWR16XX
	debugFamilyXWR18XX
)

type debugFirmwareImagePolicy uint8

const (
	debugFirmwareImageIWR6843RPRC debugFirmwareImagePolicy = iota
	debugFirmwareImageLegacyPatchRPRC
)

type debugRFEncodingPolicy uint8

const (
	debugRFEncodingIWR6843ES2 debugRFEncodingPolicy = iota
	debugRFEncodingXWR1XXX77GHz
)

type debugCapturePolicy uint8

const debugCaptureTwoLaneDCAType2 debugCapturePolicy = iota

type debugBootPolicy uint8

const (
	debugBootXWR68xxRFEval debugBootPolicy = iota
	debugBootWarmRFEval
)

type debugRuntimePolicy uint8

const (
	debugRuntimeExact debugRuntimePolicy = iota
	debugRuntimeDiagnostic
)

type debugRuntimeContract struct {
	policy debugRuntimePolicy
	mss    mmWaveLinkFirmwareRelease
	rf     mmWaveLinkFirmwareRelease
}

// debugFamilyContract is deliberately private and closed. It binds only the
// family-sensitive facts exercised by the current debug-cli path; adding a
// new family requires implementing every referenced policy before its ID can
// resolve to a canonical contract.
type debugFamilyContract struct {
	id                   debugFamilyID
	name                 string
	identity             string
	platform             string
	assets               contracts
	imagePolicy          debugFirmwareImagePolicy
	partNumbers          [9]uint8
	partCount            uint8
	rfPolicy             debugRFEncodingPolicy
	txMask               uint16
	maxChirpTransmitters uint8
	lowPowerADCMode      uint16
	capturePolicy        debugCapturePolicy
	runtime              debugRuntimeContract
	bootPolicy           debugBootPolicy
}

var iwr6843ES2DebugFamily = debugFamilyContract{
	id:       debugFamilyIWR6843ES2,
	name:     "xwr68xx",
	identity: "IWR6843 ES2",
	platform: "xWR68xx",
	assets: contracts{
		bss: fileContract{role: "BSS", name: BSSName, size: BSSSize, sha256: BSSSHA256, target: rprcTargetBSS},
		mss: fileContract{role: "MSS", name: MSSName, size: MSSSize, sha256: MSSSHA256, target: rprcTargetMSS},
	},
	imagePolicy:          debugFirmwareImageIWR6843RPRC,
	partNumbers:          [9]uint8{iwr68xxES2PartNumber},
	partCount:            1,
	rfPolicy:             debugRFEncodingIWR6843ES2,
	txMask:               0x07,
	maxChirpTransmitters: 2,
	lowPowerADCMode:      0,
	capturePolicy:        debugCaptureTwoLaneDCAType2,
	runtime: debugRuntimeContract{
		policy: debugRuntimeExact,
		mss:    mmWaveLinkFirmwareRelease{Major: 2, Minor: 0, Build: 0, Debug: 3},
		rf:     mmWaveLinkFirmwareRelease{Major: 6, Minor: 2, Build: 1, Debug: 5},
	},
	bootPolicy: debugBootXWR68xxRFEval,
}

var xwr16xxDebugFamily = debugFamilyContract{
	id:       debugFamilyXWR16XX,
	name:     "xwr16xx",
	identity: "xWR16xx",
	platform: "xWR16xx",
	assets: contracts{
		bss: fileContract{role: "BSS", name: xwr16xxBSSName, size: xwr16xxBSSSize, sha256: xwr16xxBSSSHA256, target: rprcTargetBSS},
		mss: fileContract{role: "MSS", name: xwr16xxMSSName, size: xwr16xxMSSSize, sha256: xwr16xxMSSSHA256, target: rprcTargetMSS},
	},
	imagePolicy:          debugFirmwareImageLegacyPatchRPRC,
	partNumbers:          [9]uint8{0x60, 0x61, 0x04, 0x62, 0x67, 0x66, 0x01, 0xc0, 0xc1},
	partCount:            9,
	rfPolicy:             debugRFEncodingXWR1XXX77GHz,
	txMask:               0x03,
	maxChirpTransmitters: 2,
	lowPowerADCMode:      1,
	capturePolicy:        debugCaptureTwoLaneDCAType2,
	runtime:              debugRuntimeContract{policy: debugRuntimeDiagnostic},
	bootPolicy:           debugBootWarmRFEval,
}

var xwr18xxDebugFamily = debugFamilyContract{
	id:       debugFamilyXWR18XX,
	name:     "xwr18xx",
	identity: "xWR18xx",
	platform: "xWR18xx",
	assets: contracts{
		bss: fileContract{role: "BSS", name: xwr18xxBSSName, size: xwr18xxBSSSize, sha256: xwr18xxBSSSHA256, target: rprcTargetBSS},
		mss: fileContract{role: "MSS", name: xwr18xxMSSName, size: xwr18xxMSSSize, sha256: xwr18xxMSSSHA256, target: rprcTargetMSS},
	},
	imagePolicy:          debugFirmwareImageLegacyPatchRPRC,
	partNumbers:          [9]uint8{0x70, 0x71, 0xd0, 0x05},
	partCount:            4,
	rfPolicy:             debugRFEncodingXWR1XXX77GHz,
	txMask:               0x07,
	maxChirpTransmitters: 2,
	lowPowerADCMode:      0,
	capturePolicy:        debugCaptureTwoLaneDCAType2,
	runtime:              debugRuntimeContract{policy: debugRuntimeDiagnostic},
	bootPolicy:           debugBootWarmRFEval,
}

func debugFamilyContractForID(id debugFamilyID) (debugFamilyContract, error) {
	switch id {
	case debugFamilyIWR6843ES2:
		return iwr6843ES2DebugFamily, nil
	case debugFamilyXWR16XX:
		return xwr16xxDebugFamily, nil
	case debugFamilyXWR18XX:
		return xwr18xxDebugFamily, nil
	default:
		return debugFamilyContract{}, fmt.Errorf("unsupported debug-cli family id %d", id)
	}
}

func (family debugFamilyContract) valid() bool {
	switch family.id {
	case debugFamilyIWR6843ES2:
		return family == iwr6843ES2DebugFamily
	case debugFamilyXWR16XX:
		return family == xwr16xxDebugFamily
	case debugFamilyXWR18XX:
		return family == xwr18xxDebugFamily
	default:
		return false
	}
}

// ParseDeviceFamily resolves only the three exact family names implemented by
// the debug CLI backend. It deliberately has no model aliases or empty default.
func ParseDeviceFamily(name string) (radar.DeviceFamily, error) {
	device, err := radar.ParseDeviceFamily(name)
	if err != nil {
		return radar.DeviceFamily{}, err
	}
	if _, err := debugFamilyContractForDevice(device); err != nil {
		return radar.DeviceFamily{}, err
	}
	return device, nil
}

func debugFamilyContractForDevice(device radar.DeviceFamily) (debugFamilyContract, error) {
	if !device.Valid() {
		return debugFamilyContract{}, invalidDeviceFamilyError()
	}
	var id debugFamilyID
	switch device.Name() {
	case xwr16xxDebugFamily.name:
		id = debugFamilyXWR16XX
	case xwr18xxDebugFamily.name:
		id = debugFamilyXWR18XX
	case iwr6843ES2DebugFamily.name:
		id = debugFamilyIWR6843ES2
	default:
		return debugFamilyContract{}, invalidDeviceFamilyError()
	}
	family, err := debugFamilyContractForID(id)
	if err != nil {
		return debugFamilyContract{}, err
	}
	canonical, err := radar.ParseDeviceFamily(family.name)
	if err != nil || canonical != device {
		return debugFamilyContract{}, invalidDeviceFamilyError()
	}
	return family, nil
}

func invalidDeviceFamilyError() error {
	return fmt.Errorf("invalid debug-cli device family; use exact xwr16xx, xwr18xx, or xwr68xx")
}

func (family debugFamilyContract) deviceFamily() (radar.DeviceFamily, error) {
	device, err := radar.ParseDeviceFamily(family.name)
	if err != nil {
		return radar.DeviceFamily{}, err
	}
	if !family.valid() {
		return radar.DeviceFamily{}, fmt.Errorf("invalid debug-cli family contract %d", family.id)
	}
	return device, nil
}

func (family debugFamilyContract) supportsPart(partNumber uint8) bool {
	if family.partCount == 0 || int(family.partCount) > len(family.partNumbers) {
		return false
	}
	for _, allowed := range family.partNumbers[:family.partCount] {
		if partNumber == allowed {
			return true
		}
	}
	return false
}

func (family debugFamilyContract) unsupportedPartError(partNumber uint8) error {
	if family.partCount == 1 {
		return fmt.Errorf(
			"unsupported part number 0x%02X; only validated %s part number 0x%02X is supported",
			partNumber,
			family.identity,
			family.partNumbers[0],
		)
	}
	parts := make([]string, family.partCount)
	for index, allowed := range family.partNumbers[:family.partCount] {
		parts[index] = fmt.Sprintf("0x%02X", allowed)
	}
	return fmt.Errorf(
		"unsupported %s part number 0x%02X; expected one of %s",
		family.identity,
		partNumber,
		strings.Join(parts, ", "),
	)
}

// ValidateRawCaptureFPGAConfig applies the DCA contract of the only public
// debug-cli family. It preserves the existing two-lane xWR68xx validation.
func ValidateRawCaptureFPGAConfig(config dca.FPGAConfig) error {
	device, err := radar.ParseDeviceFamily(iwr6843ES2DebugFamily.name)
	if err != nil {
		return err
	}
	return ValidateRawCaptureFPGAConfigForFamily(device, config)
}

// ValidateRawCaptureFPGAConfigForFamily applies the exact DCA/LVDS contract
// bound to one explicit debug CLI family.
func ValidateRawCaptureFPGAConfigForFamily(device radar.DeviceFamily, config dca.FPGAConfig) error {
	family, err := debugFamilyContractForDevice(device)
	if err != nil {
		return err
	}
	return family.capturePolicy.validateDCA(config)
}

func (policy debugCapturePolicy) validateDCA(config dca.FPGAConfig) error {
	switch policy {
	case debugCaptureTwoLaneDCAType2:
		return dca.ValidateRawCaptureFPGAConfig(config)
	default:
		return fmt.Errorf("unsupported debug-cli DCA policy %d", policy)
	}
}

func (policy debugCapturePolicy) laneEnablePayload() ([]byte, error) {
	switch policy {
	case debugCaptureTwoLaneDCAType2:
		return []byte{3, 0, 0, 0}, nil
	default:
		return nil, fmt.Errorf("unsupported debug-cli lane policy %d", policy)
	}
}

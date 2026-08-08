package debugcapture

import (
	"fmt"

	"mmwcli/internal/dca"
)

const iwr68xxES2PartNumber = uint8(0xe2)

type debugFamilyID uint8

const debugFamilyIWR6843ES2 debugFamilyID = iota

type debugFirmwareImagePolicy uint8

const debugFirmwareImageIWR6843RPRC debugFirmwareImagePolicy = iota

type debugRFEncodingPolicy uint8

const debugRFEncodingIWR6843ES2 debugRFEncodingPolicy = iota

type debugCapturePolicy uint8

const debugCaptureTwoLaneDCAType2 debugCapturePolicy = iota

type debugBootPolicy uint8

const debugBootXWR68xxRFEval debugBootPolicy = iota

type debugRuntimeContract struct {
	mss mmWaveLinkFirmwareRelease
	rf  mmWaveLinkFirmwareRelease
}

// debugFamilyContract is deliberately private and closed. It binds only the
// family-sensitive facts exercised by the current debug-capture path; adding a
// new family requires implementing every referenced policy before its ID can
// resolve to a canonical contract.
type debugFamilyContract struct {
	id                   debugFamilyID
	identity             string
	platform             string
	assets               contracts
	imagePolicy          debugFirmwareImagePolicy
	partNumber           uint8
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
	identity: "IWR6843 ES2",
	platform: "xWR68xx",
	assets: contracts{
		bss: fileContract{role: "BSS", name: BSSName, size: BSSSize, sha256: BSSSHA256, target: rprcTargetBSS},
		mss: fileContract{role: "MSS", name: MSSName, size: MSSSize, sha256: MSSSHA256, target: rprcTargetMSS},
	},
	imagePolicy:          debugFirmwareImageIWR6843RPRC,
	partNumber:           iwr68xxES2PartNumber,
	rfPolicy:             debugRFEncodingIWR6843ES2,
	txMask:               0x07,
	maxChirpTransmitters: 2,
	lowPowerADCMode:      0,
	capturePolicy:        debugCaptureTwoLaneDCAType2,
	runtime: debugRuntimeContract{
		mss: mmWaveLinkFirmwareRelease{Major: 2, Minor: 0, Build: 0, Debug: 3},
		rf:  mmWaveLinkFirmwareRelease{Major: 6, Minor: 2, Build: 1, Debug: 5},
	},
	bootPolicy: debugBootXWR68xxRFEval,
}

func debugFamilyContractForID(id debugFamilyID) (debugFamilyContract, error) {
	switch id {
	case debugFamilyIWR6843ES2:
		return iwr6843ES2DebugFamily, nil
	default:
		return debugFamilyContract{}, fmt.Errorf("unsupported debug-capture family id %d", id)
	}
}

func (family debugFamilyContract) valid() bool {
	switch family.id {
	case debugFamilyIWR6843ES2:
		return family == iwr6843ES2DebugFamily
	default:
		return false
	}
}

// ValidateRawCaptureFPGAConfig applies the DCA contract of the only public
// debug-capture family. It preserves the existing two-lane xWR68xx validation.
func ValidateRawCaptureFPGAConfig(config dca.FPGAConfig) error {
	family, err := debugFamilyContractForID(debugFamilyIWR6843ES2)
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
		return fmt.Errorf("unsupported debug-capture DCA policy %d", policy)
	}
}

func (policy debugCapturePolicy) laneEnablePayload() ([]byte, error) {
	switch policy {
	case debugCaptureTwoLaneDCAType2:
		return []byte{3, 0, 0, 0}, nil
	default:
		return nil, fmt.Errorf("unsupported debug-capture lane policy %d", policy)
	}
}

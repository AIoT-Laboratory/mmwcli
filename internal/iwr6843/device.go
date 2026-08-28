package iwr6843

import "fmt"

const iwr68xxES2PartNumber = uint8(0xe2)

type runtimeContract struct {
	mss mmWaveLinkFirmwareRelease
	rf  mmWaveLinkFirmwareRelease
}

const (
	iwr6843Identity             = "IWR6843 ES2"
	iwr6843Platform             = "xWR68xx"
	iwr6843PartNumber           = iwr68xxES2PartNumber
	iwr6843TXMask               = uint16(0x07)
	iwr6843MaxChirpTransmitters = uint8(2)
	iwr6843LowPowerADCMode      = uint16(0)
)

var (
	iwr6843Assets = contracts{
		bss: fileContract{role: "BSS", name: xwr68xxBSSName, size: xwr68xxBSSSize, sha256: xwr68xxBSSSHA256, target: rprcTargetBSS},
		mss: fileContract{role: "MSS", name: xwr68xxMSSName, size: xwr68xxMSSSize, sha256: xwr68xxMSSSHA256, target: rprcTargetMSS},
	}
	iwr6843Runtime = runtimeContract{
		mss: mmWaveLinkFirmwareRelease{Major: 2, Minor: 0, Build: 0, Debug: 3},
		rf:  mmWaveLinkFirmwareRelease{Major: 6, Minor: 2, Build: 1, Debug: 5},
	}
)

func supportsIWR6843Part(partNumber uint8) bool {
	return partNumber == iwr6843PartNumber
}

func unsupportedIWR6843PartError(partNumber uint8) error {
	return fmt.Errorf(
		"unsupported part number 0x%02X; expected %s part number 0x%02X",
		partNumber,
		iwr6843Identity,
		iwr6843PartNumber,
	)
}

func laneEnablePayload() []byte {
	return []byte{3, 0, 0, 0}
}

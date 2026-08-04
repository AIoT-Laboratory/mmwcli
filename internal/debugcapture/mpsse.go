package debugcapture

import "fmt"

const (
	spiDirection = byte(0x4B)
	irqDirection = byte(0x5B)
	irqMask      = byte(1 << 5)
)

func spiMPSSEInitCommand() [9]byte {
	return [9]byte{0x8A, 0x97, 0x8D, 0x86, 0x02, 0x00, 0x80, 0xC8, spiDirection}
}

func spiMPSSEWriteWord(word uint16) [12]byte {
	return [12]byte{
		0x80, 0xC0, spiDirection,
		0x11, 0x01, 0x00, byte(word >> 8), byte(word),
		0x80, 0xC8, spiDirection, 0x87,
	}
}

func spiMPSSEReadWordCommand() [10]byte {
	return [10]byte{
		0x80, 0xC2, spiDirection,
		0x20, 0x01, 0x00,
		0x80, 0xC8, spiDirection, 0x87,
	}
}

func decodeSPIWord(response []byte) (uint16, error) {
	if len(response) != 2 {
		return 0, fmt.Errorf("decode SPI word: got %d bytes, want 2", len(response))
	}
	return uint16(response[0])<<8 | uint16(response[1]), nil
}

func irqMPSSESyncCommand() [1]byte {
	return [1]byte{0xAB}
}

func validIRQMPSSESync(response []byte) bool {
	return len(response) == 2 && response[0] == 0xFA && response[1] == 0xAB
}

func irqMPSSEClockCommand() [3]byte {
	return [3]byte{0x8A, 0x97, 0x8C}
}

func irqMPSSEGPIOCommand() [6]byte {
	return [6]byte{0x80, 0x13, irqDirection, 0x86, 0x4A, 0x00}
}

func irqMPSSELoopbackOffCommand() [1]byte {
	return [1]byte{0x85}
}

func irqMPSSEReadCommand() [1]byte {
	return [1]byte{0x81}
}

func irqAsserted(sample byte) bool {
	return sample&irqMask != 0
}

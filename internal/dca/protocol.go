// Package dca implements the host-side DCA1000 UDP control and raw-data
// protocols. It intentionally leaves radar configuration and ADC processing to
// higher layers.
package dca

import (
	"encoding/binary"
	"fmt"
	"net"
)

const (
	ControlHeader          uint16 = 0xA55A
	ControlFooter          uint16 = 0xEEAA
	ControlResponseSize           = 8
	MaximumPayloadSize            = 504
	DataHeaderSize                = 10
	MaximumDataPayloadSize        = 1456
)

// Command is a DCA1000 control command code.
type Command uint16

const (
	CommandResetFPGA       Command = 0x01
	CommandResetRadar      Command = 0x02
	CommandConfigureFPGA   Command = 0x03
	CommandConfigureEEPROM Command = 0x04
	CommandStartRecord     Command = 0x05
	CommandStopRecord      Command = 0x06
	CommandStartPlayback   Command = 0x07
	CommandStopPlayback    Command = 0x08
	CommandSystemAlive     Command = 0x09
	CommandAsyncStatus     Command = 0x0A
	CommandConfigureRecord Command = 0x0B
	CommandConfigureRadar  Command = 0x0C
	CommandInitPlayback    Command = 0x0D
	CommandReadFPGAVersion Command = 0x0E
)

// System async status values are bits in the raw FPGA response status field.
// TI's host library converts each set bit back to a SYS_ASYNC_STATUS ordinal
// before invoking its callback, but mmwcli consumes the UDP wire format.
const (
	SystemStatusNoLVDSData uint16 = 1 << iota
	SystemStatusNoHeader
	SystemStatusEEPROMFailure
	SystemStatusSDCardDetected
	SystemStatusSDCardRemoved
	SystemStatusSDCardFull
	SystemStatusModeConfigFailure
	SystemStatusDDRFull
	SystemStatusRecordCompleted
	SystemStatusLVDSBufferFull
	SystemStatusPlaybackCompleted
	SystemStatusPlaybackOutOfSequence
)

const (
	systemStatusFatalMask = SystemStatusNoLVDSData |
		SystemStatusNoHeader |
		SystemStatusEEPROMFailure |
		SystemStatusModeConfigFailure |
		SystemStatusDDRFull |
		SystemStatusLVDSBufferFull
	systemStatusBenignMask = SystemStatusSDCardDetected |
		SystemStatusSDCardRemoved |
		SystemStatusSDCardFull |
		SystemStatusRecordCompleted |
		SystemStatusPlaybackCompleted |
		SystemStatusPlaybackOutOfSequence
	systemStatusKnownMask = systemStatusFatalMask | systemStatusBenignMask
)

// IsFatalSystemStatus identifies FPGA statuses that make raw Ethernet capture
// unsafe. Known SD/playback/completion notifications are irrelevant or benign
// for this mode; unknown values fail closed for the audited FPGA contract.
func IsFatalSystemStatus(status uint16) bool {
	return status == 0 || status&systemStatusFatalMask != 0 || status&^systemStatusKnownMask != 0
}

func (c Command) String() string {
	switch c {
	case CommandResetFPGA:
		return "reset-fpga"
	case CommandResetRadar:
		return "reset-radar"
	case CommandConfigureFPGA:
		return "configure-fpga"
	case CommandConfigureEEPROM:
		return "configure-eeprom"
	case CommandStartRecord:
		return "start-record"
	case CommandStopRecord:
		return "stop-record"
	case CommandStartPlayback:
		return "start-playback"
	case CommandStopPlayback:
		return "stop-playback"
	case CommandSystemAlive:
		return "system-alive"
	case CommandAsyncStatus:
		return "async-status"
	case CommandConfigureRecord:
		return "configure-record"
	case CommandConfigureRadar:
		return "configure-radar"
	case CommandInitPlayback:
		return "init-playback"
	case CommandReadFPGAVersion:
		return "read-fpga-version"
	default:
		return fmt.Sprintf("command-0x%04X", uint16(c))
	}
}

// Response is the fixed-size body of a DCA1000 control response. The UDP
// source endpoint is attached by Client.Execute, not ParseResponse.
type Response struct {
	Command Command
	Status  uint16
	Source  *net.UDPAddr
}

// BuildRequest encodes a DCA1000 request using the little-endian
// 0xA55A ... 0xEEAA framing.
func BuildRequest(command Command, payload []byte) ([]byte, error) {
	if len(payload) > MaximumPayloadSize {
		return nil, fmt.Errorf("DCA1000 command payload is %d bytes; maximum is %d", len(payload), MaximumPayloadSize)
	}
	packet := make([]byte, ControlResponseSize+len(payload))
	binary.LittleEndian.PutUint16(packet[0:2], ControlHeader)
	binary.LittleEndian.PutUint16(packet[2:4], uint16(command))
	binary.LittleEndian.PutUint16(packet[4:6], uint16(len(payload)))
	copy(packet[6:6+len(payload)], payload)
	binary.LittleEndian.PutUint16(packet[6+len(payload):], ControlFooter)
	return packet, nil
}

// ParseResponse decodes a control response. Responses must be exactly eight
// bytes; request frames and truncated or extended UDP datagrams are rejected.
func ParseResponse(packet []byte) (Response, error) {
	if len(packet) != ControlResponseSize {
		return Response{}, fmt.Errorf("DCA1000 response must be exactly %d bytes, got %d", ControlResponseSize, len(packet))
	}
	if header := binary.LittleEndian.Uint16(packet[0:2]); header != ControlHeader {
		return Response{}, fmt.Errorf("invalid DCA1000 response header 0x%04X", header)
	}
	if footer := binary.LittleEndian.Uint16(packet[6:8]); footer != ControlFooter {
		return Response{}, fmt.Errorf("invalid DCA1000 response footer 0x%04X", footer)
	}
	return Response{
		Command: Command(binary.LittleEndian.Uint16(packet[2:4])),
		Status:  binary.LittleEndian.Uint16(packet[4:6]),
	}, nil
}

// FPGAConfig is the six-byte payload of CommandConfigureFPGA.
type FPGAConfig struct {
	LogMode      int
	LVDSMode     int
	TransferMode int
	CaptureMode  int
	DataFormat   int
	Timer        int
}

// DefaultFPGAConfig returns the xWR68xx raw-capture defaults used by mmwcli:
// two LVDS lanes and 16-bit complex data.
func DefaultFPGAConfig() FPGAConfig {
	return FPGAConfig{
		LogMode:      1,
		LVDSMode:     2,
		TransferMode: 1,
		CaptureMode:  2,
		DataFormat:   3,
		Timer:        30,
	}
}

// ValidateRawCaptureFPGAConfig enforces the xWR68xx receiver contract used by
// both integrated and independent captures. Configure remains a lower-level
// command and may send other valid FPGA enum values, but the raw UDP receiver
// does not model multi-mode, playback, or SD-card data paths.
func ValidateRawCaptureFPGAConfig(config FPGAConfig) error {
	if config.LogMode != 1 || config.LVDSMode != 2 || config.TransferMode != 1 ||
		config.CaptureMode != 2 || config.DataFormat != 3 || config.Timer != 30 {
		return fmt.Errorf(
			"xWR68xx raw capture requires DCA log-mode=1, lvds-mode=2, transfer-mode=1, capture-mode=2, data-format=3, and timer=30",
		)
	}
	return nil
}

// BuildFPGAConfig validates and encodes an FPGA configuration payload.
func BuildFPGAConfig(config FPGAConfig) ([]byte, error) {
	checks := []struct {
		name    string
		value   int
		minimum int
		maximum int
	}{
		{"log mode", config.LogMode, 1, 2},
		{"LVDS mode", config.LVDSMode, 1, 2},
		{"transfer mode", config.TransferMode, 1, 2},
		{"capture mode", config.CaptureMode, 1, 2},
		{"data format", config.DataFormat, 1, 3},
		{"timer", config.Timer, 0, 255},
	}
	for _, check := range checks {
		if check.value < check.minimum || check.value > check.maximum {
			return nil, fmt.Errorf("DCA1000 %s must be in %d..%d, got %d", check.name, check.minimum, check.maximum, check.value)
		}
	}
	return []byte{
		byte(config.LogMode),
		byte(config.LVDSMode),
		byte(config.TransferMode),
		byte(config.CaptureMode),
		byte(config.DataFormat),
		byte(config.Timer),
	}, nil
}

// BuildRecordConfig encodes the DCA1000 packet-size and packet-delay payload.
// The FPGA delay field is clocked at 125 MHz, so each microsecond is 125 ticks.
func BuildRecordConfig(delayMicroseconds int) ([]byte, error) {
	if delayMicroseconds < 5 || delayMicroseconds > 500 {
		return nil, fmt.Errorf("DCA1000 packet delay must be in 5..500 microseconds, got %d", delayMicroseconds)
	}
	const packetSize = uint16(1470)
	delayTicks := uint16(delayMicroseconds * 125)
	payload := make([]byte, 6)
	binary.LittleEndian.PutUint16(payload[0:2], packetSize)
	binary.LittleEndian.PutUint16(payload[2:4], delayTicks)
	// payload[4:6] is reserved and remains zero.
	return payload, nil
}

// DataPacket is a decoded DCA1000 data datagram. Payload aliases the input
// datagram and must be copied if it needs to outlive that buffer.
type DataPacket struct {
	Sequence   uint32
	ByteOffset uint64
	Payload    []byte
}

// ParseDataPacket decodes the 32-bit sequence and little-endian 48-bit byte
// offset that prefix every raw ADC UDP datagram.
func ParseDataPacket(datagram []byte) (DataPacket, error) {
	if len(datagram) <= DataHeaderSize {
		return DataPacket{}, fmt.Errorf("DCA1000 data datagram must contain a %d-byte header and a non-empty payload", DataHeaderSize)
	}
	if payloadSize := len(datagram) - DataHeaderSize; payloadSize > MaximumDataPayloadSize {
		return DataPacket{}, fmt.Errorf(
			"DCA1000 data payload must not exceed %d bytes, got %d",
			MaximumDataPayloadSize,
			payloadSize,
		)
	}
	offset := uint64(0)
	for index := range 6 {
		offset |= uint64(datagram[4+index]) << (8 * index)
	}
	return DataPacket{
		Sequence:   binary.LittleEndian.Uint32(datagram[0:4]),
		ByteOffset: offset,
		Payload:    datagram[DataHeaderSize:],
	}, nil
}

// FPGAVersion is the decoded value returned in the status field of
// CommandReadFPGAVersion.
type FPGAVersion struct {
	Major    uint8
	Minor    uint8
	Playback bool
}

func DecodeFPGAVersion(encoded uint16) FPGAVersion {
	return FPGAVersion{
		Major:    uint8(encoded & 0x7F),
		Minor:    uint8((encoded >> 7) & 0x7F),
		Playback: encoded&0x4000 != 0,
	}
}

func (version FPGAVersion) String() string {
	mode := "Record"
	if version.Playback {
		mode = "Playback"
	}
	return fmt.Sprintf("%d.%d (%s)", version.Major, version.Minor, mode)
}

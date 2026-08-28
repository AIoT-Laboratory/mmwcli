package iwr6843

import (
	"encoding/binary"
	"fmt"
)

const (
	rhcpSyncLength                   = 4
	rhcpHeaderLength                 = 12
	rhcpCRC16Length                  = 2
	rhcpMaxMessageLength             = 256
	rhcpMaxCommandPayloadLength      = 232
	rhcpMaxSubblocks                 = 32
	rhcpSubblockHeaderLength         = 4
	rhcpProtocolAlignment            = 4
	rhcpProtocolDummyByte       byte = 0xff
	rhcpHostToDeviceSyncWord1        = 0x1234
	rhcpHostToDeviceSyncWord2        = 0x4321
	rhcpDeviceToHostSyncWord1        = 0xdcba
	rhcpDeviceToHostSyncWord2        = 0xabcd

	rhcpOpcodeDirectionMask = 0x000f
	rhcpOpcodeClassMask     = 0x0030
	rhcpOpcodeMessageIDMask = 0xffc0

	rhcpFlagRetryMask    = 0x0003
	rhcpFlagACKMask      = 0x000c
	rhcpFlagVersionMask  = 0x00f0
	rhcpFlagCRCMask      = 0x0300
	rhcpFlagCRCLenMask   = 0x0c00
	rhcpFlagSequenceMask = 0xf000
)

type rhcpDirection uint8

const (
	rhcpDirectionHostToBSS rhcpDirection = 1
	rhcpDirectionBSSToHost rhcpDirection = 2
	rhcpDirectionHostToMSS rhcpDirection = 5
	rhcpDirectionMSSToHost rhcpDirection = 6
)

type rhcpMessageClass uint8

const (
	rhcpMessageClassCommand rhcpMessageClass = iota
	rhcpMessageClassResponse
	rhcpMessageClassNACK
	rhcpMessageClassAsync
)

type mmWaveLinkSubblock struct {
	id   uint16
	data []byte
}

type mmWaveLinkCommand struct {
	direction       rhcpDirection
	messageID       uint16
	sequence        uint8
	remainingChunks uint16
	subblocks       []mmWaveLinkSubblock
}

type mmWaveLinkMessage struct {
	direction       rhcpDirection
	messageClass    rhcpMessageClass
	messageID       uint16
	sequence        uint8
	remainingChunks uint16
	subblocks       []mmWaveLinkSubblock
}

func encodeMMWaveLinkCommand(command mmWaveLinkCommand) ([]byte, error) {
	if command.direction != rhcpDirectionHostToBSS && command.direction != rhcpDirectionHostToMSS {
		return nil, fmt.Errorf("invalid RHCP command direction %d", command.direction)
	}
	if command.messageID > 0x03ff {
		return nil, fmt.Errorf("RHCP message ID %#x exceeds 10 bits", command.messageID)
	}
	if command.sequence > 0x0f {
		return nil, fmt.Errorf("RHCP sequence %d exceeds 4 bits", command.sequence)
	}
	if len(command.subblocks) > rhcpMaxSubblocks {
		return nil, fmt.Errorf("RHCP command has %d sub-blocks; maximum is %d", len(command.subblocks), rhcpMaxSubblocks)
	}

	payloadLength := 0
	for index, subblock := range command.subblocks {
		if subblock.id >= rhcpMaxSubblocks {
			return nil, fmt.Errorf("RHCP sub-block %d ID %d exceeds 5 bits", index, subblock.id)
		}
		payloadLength += rhcpSubblockHeaderLength + len(subblock.data)
		if payloadLength > rhcpMaxCommandPayloadLength {
			return nil, fmt.Errorf("RHCP command payload is %d bytes; maximum is %d", payloadLength, rhcpMaxCommandPayloadLength)
		}
	}

	paddingLength := (rhcpProtocolAlignment - ((rhcpHeaderLength + payloadLength) % rhcpProtocolAlignment)) % rhcpProtocolAlignment
	if payloadLength+paddingLength > rhcpMaxCommandPayloadLength {
		return nil, fmt.Errorf(
			"RHCP command payload is %d bytes after alignment; maximum is %d",
			payloadLength+paddingLength,
			rhcpMaxCommandPayloadLength,
		)
	}

	headerMessageLength := rhcpHeaderLength + payloadLength + paddingLength + rhcpCRC16Length
	frame := make([]byte, rhcpSyncLength+headerMessageLength)
	binary.LittleEndian.PutUint16(frame[0:2], rhcpHostToDeviceSyncWord1)
	binary.LittleEndian.PutUint16(frame[2:4], rhcpHostToDeviceSyncWord2)

	opcode := uint16(command.direction) |
		(uint16(rhcpMessageClassCommand) << 4) |
		(command.messageID << 6)
	binary.LittleEndian.PutUint16(frame[4:6], opcode)
	binary.LittleEndian.PutUint16(frame[6:8], uint16(headerMessageLength))
	binary.LittleEndian.PutUint16(frame[8:10], uint16(command.sequence)<<12)
	binary.LittleEndian.PutUint16(frame[10:12], command.remainingChunks)
	binary.LittleEndian.PutUint16(frame[12:14], uint16(len(command.subblocks)))

	offset := rhcpSyncLength + rhcpHeaderLength
	for _, subblock := range command.subblocks {
		uniqueID := command.messageID*rhcpMaxSubblocks + subblock.id
		binary.LittleEndian.PutUint16(frame[offset:offset+2], uniqueID)
		binary.LittleEndian.PutUint16(frame[offset+2:offset+4], uint16(rhcpSubblockHeaderLength+len(subblock.data)))
		copy(frame[offset+rhcpSubblockHeaderLength:], subblock.data)
		offset += rhcpSubblockHeaderLength + len(subblock.data)
	}
	for range paddingLength {
		frame[offset] = rhcpProtocolDummyByte
		offset++
	}

	binary.LittleEndian.PutUint16(frame[14:16], rhcpHeaderChecksum(frame[4:14]))
	binary.LittleEndian.PutUint16(frame[len(frame)-rhcpCRC16Length:], rhcpCRC16CCITTFalse(frame[4:len(frame)-rhcpCRC16Length]))
	return frame, nil
}

func decodeMMWaveLinkMessage(frame []byte) (mmWaveLinkMessage, error) {
	var message mmWaveLinkMessage
	minimumLength := rhcpSyncLength + rhcpHeaderLength + rhcpCRC16Length
	if len(frame) < minimumLength {
		return message, fmt.Errorf("RHCP message is %d bytes; minimum is %d", len(frame), minimumLength)
	}
	if len(frame) > rhcpMaxMessageLength {
		return message, fmt.Errorf("RHCP message is %d bytes; maximum is %d", len(frame), rhcpMaxMessageLength)
	}
	if binary.LittleEndian.Uint16(frame[0:2]) != rhcpDeviceToHostSyncWord1 ||
		binary.LittleEndian.Uint16(frame[2:4]) != rhcpDeviceToHostSyncWord2 {
		return message, fmt.Errorf("invalid RHCP device-to-host sync % x", frame[:rhcpSyncLength])
	}

	declaredLength := int(binary.LittleEndian.Uint16(frame[6:8]))
	if declaredLength != len(frame)-rhcpSyncLength {
		return message, fmt.Errorf("RHCP header length is %d; received %d bytes after sync", declaredLength, len(frame)-rhcpSyncLength)
	}
	if declaredLength < rhcpHeaderLength+rhcpCRC16Length {
		return message, fmt.Errorf("RHCP header length is %d; minimum is %d", declaredLength, rhcpHeaderLength+rhcpCRC16Length)
	}
	if (declaredLength-rhcpCRC16Length)%rhcpProtocolAlignment != 0 {
		return message, fmt.Errorf("RHCP body length %d is not %d-byte aligned", declaredLength-rhcpCRC16Length, rhcpProtocolAlignment)
	}

	wantHeaderChecksum := rhcpHeaderChecksum(frame[4:14])
	gotHeaderChecksum := binary.LittleEndian.Uint16(frame[14:16])
	if gotHeaderChecksum != wantHeaderChecksum {
		return message, fmt.Errorf("RHCP header checksum is %#04x; expected %#04x", gotHeaderChecksum, wantHeaderChecksum)
	}

	opcode := binary.LittleEndian.Uint16(frame[4:6])
	message.direction = rhcpDirection(opcode & rhcpOpcodeDirectionMask)
	if message.direction != rhcpDirectionBSSToHost && message.direction != rhcpDirectionMSSToHost {
		return message, fmt.Errorf("invalid RHCP device-to-host direction %d", message.direction)
	}
	message.messageClass = rhcpMessageClass((opcode & rhcpOpcodeClassMask) >> 4)
	if message.messageClass == rhcpMessageClassCommand {
		return message, fmt.Errorf("invalid RHCP inbound message class %d", message.messageClass)
	}
	message.messageID = (opcode & rhcpOpcodeMessageIDMask) >> 6

	flags := binary.LittleEndian.Uint16(frame[8:10])
	if retry := flags & rhcpFlagRetryMask; retry != 0 && retry != rhcpFlagRetryMask {
		return message, fmt.Errorf("invalid RHCP retry flag %#x", retry)
	}
	if ack := flags & rhcpFlagACKMask; ack != 0 && ack != rhcpFlagACKMask {
		return message, fmt.Errorf("invalid RHCP ACK flag %#x", ack>>2)
	}
	if version := flags & rhcpFlagVersionMask; version != 0 {
		return message, fmt.Errorf("unsupported RHCP protocol version %d", version>>4)
	}
	if crcFlag := flags & rhcpFlagCRCMask; crcFlag != 0 {
		return message, fmt.Errorf("RHCP message does not declare a CRC")
	}
	if crcLength := flags & rhcpFlagCRCLenMask; crcLength != 0 {
		return message, fmt.Errorf("unsupported RHCP CRC length flag %d", crcLength>>10)
	}
	message.sequence = uint8((flags & rhcpFlagSequenceMask) >> 12)
	message.remainingChunks = binary.LittleEndian.Uint16(frame[10:12])

	wantCRC := rhcpCRC16CCITTFalse(frame[4 : len(frame)-rhcpCRC16Length])
	gotCRC := binary.LittleEndian.Uint16(frame[len(frame)-rhcpCRC16Length:])
	if gotCRC != wantCRC {
		return message, fmt.Errorf("RHCP CRC16 is %#04x; expected %#04x", gotCRC, wantCRC)
	}

	subblockCount := int(binary.LittleEndian.Uint16(frame[12:14]))
	if subblockCount > rhcpMaxSubblocks {
		return message, fmt.Errorf("RHCP message has %d sub-blocks; maximum is %d", subblockCount, rhcpMaxSubblocks)
	}
	payload := frame[rhcpSyncLength+rhcpHeaderLength : len(frame)-rhcpCRC16Length]
	message.subblocks = make([]mmWaveLinkSubblock, 0, subblockCount)
	for index := range subblockCount {
		if len(payload) < rhcpSubblockHeaderLength {
			return mmWaveLinkMessage{}, fmt.Errorf("RHCP sub-block %d header is truncated", index)
		}
		uniqueID := binary.LittleEndian.Uint16(payload[0:2])
		subblockLength := int(binary.LittleEndian.Uint16(payload[2:4]))
		if subblockLength < rhcpSubblockHeaderLength {
			return mmWaveLinkMessage{}, fmt.Errorf("RHCP sub-block %d length is %d; minimum is %d", index, subblockLength, rhcpSubblockHeaderLength)
		}
		if subblockLength > len(payload) {
			return mmWaveLinkMessage{}, fmt.Errorf("RHCP sub-block %d length is %d; only %d payload bytes remain", index, subblockLength, len(payload))
		}
		if uniqueID/rhcpMaxSubblocks != message.messageID {
			return mmWaveLinkMessage{}, fmt.Errorf("RHCP sub-block %d unique ID %#04x does not match message ID %#03x", index, uniqueID, message.messageID)
		}
		message.subblocks = append(message.subblocks, mmWaveLinkSubblock{
			id:   uniqueID % rhcpMaxSubblocks,
			data: append([]byte(nil), payload[rhcpSubblockHeaderLength:subblockLength]...),
		})
		payload = payload[subblockLength:]
	}
	if len(payload) >= rhcpProtocolAlignment {
		return mmWaveLinkMessage{}, fmt.Errorf("RHCP message has %d unclaimed payload bytes", len(payload))
	}
	for _, value := range payload {
		if value != rhcpProtocolDummyByte {
			return mmWaveLinkMessage{}, fmt.Errorf("invalid RHCP padding byte %#02x", value)
		}
	}
	return message, nil
}

func rhcpHeaderChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.LittleEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) != 0 {
		sum += uint32(data[0])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func rhcpCRC16CCITTFalse(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

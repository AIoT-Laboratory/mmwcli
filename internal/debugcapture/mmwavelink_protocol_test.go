package debugcapture

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestEncodeMMWaveLinkCommandMatchesStudioGoldenFrame(t *testing.T) {
	frame, err := encodeMMWaveLinkCommand(mmWaveLinkCommand{
		direction: rhcpDirectionHostToMSS,
		messageID: 0x200,
		sequence:  4,
		subblocks: []mmWaveLinkSubblock{{id: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := littleEndianWords(
		0x1234, 0x4321,
		0x8005, 0x0012, 0x4000, 0x0000, 0x0001, 0x3fe7,
		0x4000, 0x0004,
		0xdeda,
	)
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame = % X\nwant  = % X", frame, want)
	}
	if got := rhcpCRC16CCITTFalse(frame[4 : len(frame)-rhcpCRC16Length]); got != 0xdeda {
		t.Fatalf("CRC16 = %#04x, want %#04x", got, 0xdeda)
	}
}

func TestDecodeMMWaveLinkResponseMatchesStudioGoldenFrame(t *testing.T) {
	frame := littleEndianWords(
		0xdcba, 0xabcd,
		0x8016, 0x000e, 0x400c, 0x0000, 0x0000, 0x3fcf,
		0x0544,
	)
	message, err := decodeMMWaveLinkMessage(frame)
	if err != nil {
		t.Fatal(err)
	}
	if message.direction != rhcpDirectionMSSToHost ||
		message.messageClass != rhcpMessageClassResponse ||
		message.messageID != 0x200 ||
		message.sequence != 4 ||
		message.remainingChunks != 0 ||
		len(message.subblocks) != 0 {
		t.Fatalf("decoded message = %+v", message)
	}
	if got := rhcpHeaderChecksum(frame[4:14]); got != 0x3fcf {
		t.Fatalf("header checksum = %#04x, want %#04x", got, 0x3fcf)
	}
	if got := rhcpCRC16CCITTFalse(frame[4 : len(frame)-rhcpCRC16Length]); got != 0x0544 {
		t.Fatalf("CRC16 = %#04x, want %#04x", got, 0x0544)
	}
}

func TestDecodeMMWaveLinkMessageCopiesSubblocksAndConsumesPadding(t *testing.T) {
	frame := testInboundMMWaveLinkFrame(t, 0x280, rhcpMessageClassAsync, 7, []mmWaveLinkSubblock{
		{id: 0, data: []byte{0x11, 0x22}},
		{id: 3, data: []byte{0x33, 0x44, 0x55, 0x66}},
	})
	message, err := decodeMMWaveLinkMessage(frame)
	if err != nil {
		t.Fatal(err)
	}
	if message.direction != rhcpDirectionMSSToHost ||
		message.messageClass != rhcpMessageClassAsync ||
		message.messageID != 0x280 ||
		message.sequence != 7 ||
		len(message.subblocks) != 2 {
		t.Fatalf("decoded message = %+v", message)
	}
	if message.subblocks[0].id != 0 || !bytes.Equal(message.subblocks[0].data, []byte{0x11, 0x22}) {
		t.Fatalf("first sub-block = %+v", message.subblocks[0])
	}
	if message.subblocks[1].id != 3 || !bytes.Equal(message.subblocks[1].data, []byte{0x33, 0x44, 0x55, 0x66}) {
		t.Fatalf("second sub-block = %+v", message.subblocks[1])
	}

	frame[20] ^= 0xff
	if !bytes.Equal(message.subblocks[0].data, []byte{0x11, 0x22}) {
		t.Fatalf("decoded sub-block aliases input frame: % X", message.subblocks[0].data)
	}
}

func TestDecodeMMWaveLinkMessageRejectsWireCorruption(t *testing.T) {
	golden := littleEndianWords(
		0xdcba, 0xabcd,
		0x8016, 0x000e, 0x400c, 0x0000, 0x0000, 0x3fcf,
		0x0544,
	)
	tests := []struct {
		name    string
		mutate  func([]byte) []byte
		wantErr string
	}{
		{
			name: "sync",
			mutate: func(frame []byte) []byte {
				frame[0] ^= 0x01
				return frame
			},
			wantErr: "sync",
		},
		{
			name: "declared length",
			mutate: func(frame []byte) []byte {
				binary.LittleEndian.PutUint16(frame[6:8], 16)
				return frame
			},
			wantErr: "header length",
		},
		{
			name: "header checksum",
			mutate: func(frame []byte) []byte {
				frame[14] ^= 0x01
				return frame
			},
			wantErr: "header checksum",
		},
		{
			name: "CRC",
			mutate: func(frame []byte) []byte {
				frame[len(frame)-1] ^= 0x01
				return frame
			},
			wantErr: "CRC16",
		},
		{
			name: "truncated",
			mutate: func(frame []byte) []byte {
				return frame[:len(frame)-1]
			},
			wantErr: "minimum",
		},
		{
			name: "too large",
			mutate: func([]byte) []byte {
				frame := make([]byte, rhcpMaxMessageLength+1)
				binary.LittleEndian.PutUint16(frame[0:2], rhcpDeviceToHostSyncWord1)
				binary.LittleEndian.PutUint16(frame[2:4], rhcpDeviceToHostSyncWord2)
				return frame
			},
			wantErr: "maximum",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := append([]byte(nil), golden...)
			_, err := decodeMMWaveLinkMessage(test.mutate(frame))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("decode error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestDecodeMMWaveLinkMessageRejectsMalformedPayload(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func([]byte)
		wantErr string
	}{
		{
			name: "payload corruption",
			mutate: func(frame []byte) {
				frame[rhcpSyncLength+rhcpHeaderLength+rhcpSubblockHeaderLength] ^= 0x01
			},
			wantErr: "CRC16",
		},
		{
			name: "sub-block shorter than header",
			mutate: func(frame []byte) {
				binary.LittleEndian.PutUint16(frame[18:20], 3)
				rewriteTestFrameCRC(frame)
			},
			wantErr: "minimum",
		},
		{
			name: "sub-block extends past payload",
			mutate: func(frame []byte) {
				binary.LittleEndian.PutUint16(frame[18:20], 200)
				rewriteTestFrameCRC(frame)
			},
			wantErr: "remain",
		},
		{
			name: "sub-block message mismatch",
			mutate: func(frame []byte) {
				binary.LittleEndian.PutUint16(frame[16:18], 0)
				rewriteTestFrameCRC(frame)
			},
			wantErr: "does not match",
		},
		{
			name: "invalid padding",
			mutate: func(frame []byte) {
				frame[len(frame)-rhcpCRC16Length-1] = 0
				rewriteTestFrameCRC(frame)
			},
			wantErr: "padding",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := testInboundMMWaveLinkFrame(t, 0x280, rhcpMessageClassAsync, 0, []mmWaveLinkSubblock{
				{id: 0, data: []byte{0x11, 0x22}},
			})
			test.mutate(frame)
			_, err := decodeMMWaveLinkMessage(frame)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("decode error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestEncodeMMWaveLinkCommandRejectsProtocolLimits(t *testing.T) {
	tests := []struct {
		name    string
		command mmWaveLinkCommand
		wantErr string
	}{
		{
			name:    "direction",
			command: mmWaveLinkCommand{direction: rhcpDirectionBSSToHost},
			wantErr: "direction",
		},
		{
			name:    "message ID",
			command: mmWaveLinkCommand{direction: rhcpDirectionHostToBSS, messageID: 0x400},
			wantErr: "message ID",
		},
		{
			name:    "sequence",
			command: mmWaveLinkCommand{direction: rhcpDirectionHostToBSS, sequence: 16},
			wantErr: "sequence",
		},
		{
			name: "sub-block count",
			command: mmWaveLinkCommand{
				direction: rhcpDirectionHostToBSS,
				subblocks: make([]mmWaveLinkSubblock, rhcpMaxSubblocks+1),
			},
			wantErr: "sub-blocks",
		},
		{
			name: "sub-block ID",
			command: mmWaveLinkCommand{
				direction: rhcpDirectionHostToBSS,
				subblocks: []mmWaveLinkSubblock{{id: rhcpMaxSubblocks}},
			},
			wantErr: "ID",
		},
		{
			name: "payload",
			command: mmWaveLinkCommand{
				direction: rhcpDirectionHostToBSS,
				subblocks: []mmWaveLinkSubblock{{data: make([]byte, rhcpMaxCommandPayloadLength)}},
			},
			wantErr: "payload",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := encodeMMWaveLinkCommand(test.command); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("encode error = %v, want text %q", err, test.wantErr)
			}
		})
	}
}

func TestRHCPHeaderChecksumFoldsCarry(t *testing.T) {
	data := littleEndianWords(0xffff, 0x0001)
	if got := rhcpHeaderChecksum(data); got != 0xfffe {
		t.Fatalf("checksum = %#04x, want %#04x", got, 0xfffe)
	}
}

func testInboundMMWaveLinkFrame(
	t *testing.T,
	messageID uint16,
	messageClass rhcpMessageClass,
	sequence uint8,
	subblocks []mmWaveLinkSubblock,
) []byte {
	t.Helper()
	payloadLength := 0
	for _, subblock := range subblocks {
		payloadLength += rhcpSubblockHeaderLength + len(subblock.data)
	}
	paddingLength := (rhcpProtocolAlignment - ((rhcpHeaderLength + payloadLength) % rhcpProtocolAlignment)) % rhcpProtocolAlignment
	declaredLength := rhcpHeaderLength + payloadLength + paddingLength + rhcpCRC16Length
	frame := make([]byte, rhcpSyncLength+declaredLength)
	binary.LittleEndian.PutUint16(frame[0:2], rhcpDeviceToHostSyncWord1)
	binary.LittleEndian.PutUint16(frame[2:4], rhcpDeviceToHostSyncWord2)
	binary.LittleEndian.PutUint16(frame[4:6], uint16(rhcpDirectionMSSToHost)|uint16(messageClass)<<4|messageID<<6)
	binary.LittleEndian.PutUint16(frame[6:8], uint16(declaredLength))
	binary.LittleEndian.PutUint16(frame[8:10], uint16(sequence)<<12|rhcpFlagACKMask)
	binary.LittleEndian.PutUint16(frame[12:14], uint16(len(subblocks)))

	offset := rhcpSyncLength + rhcpHeaderLength
	for _, subblock := range subblocks {
		binary.LittleEndian.PutUint16(frame[offset:offset+2], messageID*rhcpMaxSubblocks+subblock.id)
		binary.LittleEndian.PutUint16(frame[offset+2:offset+4], uint16(rhcpSubblockHeaderLength+len(subblock.data)))
		copy(frame[offset+rhcpSubblockHeaderLength:], subblock.data)
		offset += rhcpSubblockHeaderLength + len(subblock.data)
	}
	for range paddingLength {
		frame[offset] = rhcpProtocolDummyByte
		offset++
	}
	binary.LittleEndian.PutUint16(frame[14:16], rhcpHeaderChecksum(frame[4:14]))
	rewriteTestFrameCRC(frame)
	return frame
}

func rewriteTestFrameCRC(frame []byte) {
	binary.LittleEndian.PutUint16(
		frame[len(frame)-rhcpCRC16Length:],
		rhcpCRC16CCITTFalse(frame[4:len(frame)-rhcpCRC16Length]),
	)
}

func littleEndianWords(words ...uint16) []byte {
	result := make([]byte, len(words)*2)
	for index, word := range words {
		binary.LittleEndian.PutUint16(result[index*2:], word)
	}
	return result
}

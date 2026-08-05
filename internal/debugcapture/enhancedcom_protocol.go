package debugcapture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const enhancedCOMMaximumBlockSize = 4096

func encodeEnhancedCOMWake() []byte {
	return []byte("x0 \r\n")
}

func encodeEnhancedCOMRead(address uint32) []byte {
	return []byte(fmt.Sprintf("rd %08x\r", address))
}

func encodeEnhancedCOMRegisterWrite(address, value uint32) []byte {
	return []byte(fmt.Sprintf("wr %08x %08x\r", address, value))
}

func encodeEnhancedCOMBlockWrite(address uint32, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("Enhanced COM block is empty")
	}
	if len(data) > enhancedCOMMaximumBlockSize {
		return nil, fmt.Errorf(
			"Enhanced COM block size %d exceeds maximum %d",
			len(data),
			enhancedCOMMaximumBlockSize,
		)
	}
	if address%4 != 0 {
		return nil, fmt.Errorf("Enhanced COM block address is not 4-byte aligned: 0x%08X", address)
	}
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("Enhanced COM block size is not 4-byte aligned: %d", len(data))
	}
	if uint64(address)+uint64(len(data)) > uint64(1)<<32 {
		return nil, fmt.Errorf(
			"Enhanced COM block exceeds 32-bit address space: address=0x%08X size=%d",
			address,
			len(data),
		)
	}

	var command strings.Builder
	command.Grow(13 + len(data)/4*9)
	_, _ = fmt.Fprintf(&command, "wr %08x ", address)
	for offset := 0; offset < len(data); offset += 4 {
		_, _ = fmt.Fprintf(&command, "%08x ", binary.LittleEndian.Uint32(data[offset:offset+4]))
	}
	_ = command.WriteByte('\r')
	return []byte(command.String()), nil
}

func parseEnhancedCOMReadResponse(response []byte) (uint32, error) {
	value := trimEnhancedCOMWhitespace(response)
	if len(value) == 0 {
		return 0, errors.New("Enhanced COM read response is empty")
	}
	if len(value) != 8 {
		return 0, fmt.Errorf("Enhanced COM read response must contain exactly 8 hex digits; got %d", len(value))
	}
	for _, character := range value {
		if !isASCIIHexDigit(character) {
			return 0, fmt.Errorf("Enhanced COM read response contains non-hex byte 0x%02X", character)
		}
	}
	parsed, err := strconv.ParseUint(string(value), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("parse Enhanced COM read response: %w", err)
	}
	return uint32(parsed), nil
}

func trimEnhancedCOMWhitespace(value []byte) []byte {
	start := 0
	for start < len(value) && isEnhancedCOMWhitespace(value[start]) {
		start++
	}
	end := len(value)
	for end > start && isEnhancedCOMWhitespace(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isEnhancedCOMWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isASCIIHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

package debugcapture

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestEnhancedCOMCommandEncodingMatchesStudioWireFormat(t *testing.T) {
	if got, want := string(encodeEnhancedCOMWake()), "x0 \r\n"; got != want {
		t.Fatalf("wake command = %q, want %q", got, want)
	}
	if got, want := string(encodeEnhancedCOMRead(0xffffe1dc)), "rd ffffe1dc\r"; got != want {
		t.Fatalf("read command = %q, want %q", got, want)
	}
	if got, want := string(encodeEnhancedCOMRegisterWrite(0xffffe108, 0xadad00ad)), "wr ffffe108 adad00ad\r"; got != want {
		t.Fatalf("register write command = %q, want %q", got, want)
	}

	got, err := encodeEnhancedCOMBlockWrite(0x40001000, []byte{
		0x01, 0x02, 0x03, 0x04,
		0xff, 0x00, 0xaa, 0x55,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "wr 40001000 04030201 55aa00ff \r"
	if string(got) != want {
		t.Fatalf("block write command = %q, want %q", got, want)
	}
}

func TestEnhancedCOMBlockEncodingAcceptsProtocolBoundaries(t *testing.T) {
	data := bytes.Repeat([]byte{0xef, 0xbe, 0xad, 0xde}, enhancedCOMMaximumBlockSize/4)
	command, err := encodeEnhancedCOMBlockWrite(0xfffff000, data)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(command), 13+enhancedCOMMaximumBlockSize/4*9; got != want {
		t.Fatalf("command size = %d, want %d", got, want)
	}
	if !strings.HasPrefix(string(command), "wr fffff000 deadbeef ") || !strings.HasSuffix(string(command), "deadbeef \r") {
		t.Fatalf("unexpected boundary command framing: %q...%q", command[:21], command[len(command)-11:])
	}

	if _, err := encodeEnhancedCOMBlockWrite(0xfffffffc, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("last aligned word was rejected: %v", err)
	}
}

func TestEnhancedCOMBlockEncodingRejectsInvalidWrites(t *testing.T) {
	tests := []struct {
		name    string
		address uint32
		data    []byte
		want    string
	}{
		{name: "empty", data: nil, want: "empty"},
		{name: "oversized", data: make([]byte, enhancedCOMMaximumBlockSize+4), want: "exceeds maximum"},
		{name: "unaligned address", address: 1, data: make([]byte, 4), want: "address is not 4-byte aligned"},
		{name: "unaligned size", data: make([]byte, 3), want: "size is not 4-byte aligned"},
		{name: "address wrap", address: 0xfffffffc, data: make([]byte, 8), want: "exceeds 32-bit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := encodeEnhancedCOMBlockWrite(test.address, test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseEnhancedCOMReadResponse(t *testing.T) {
	tests := []struct {
		response string
		want     uint32
	}{
		{response: "2", want: 2},
		{response: "12345\r\n", want: 0x12345},
		{response: "00000000", want: 0},
		{response: "ffffffff", want: 0xffffffff},
		{response: " \r\nAd010100\t", want: 0xad010100},
	}
	for _, test := range tests {
		got, err := parseEnhancedCOMReadResponse([]byte(test.response))
		if err != nil || got != test.want {
			t.Fatalf("parseEnhancedCOMReadResponse(%q) = (0x%08X, %v), want (0x%08X, nil)", test.response, got, err, test.want)
		}
	}
}

func TestParseEnhancedCOMReadResponseRejectsAmbiguousInput(t *testing.T) {
	tests := []struct {
		name     string
		response []byte
	}{
		{name: "empty", response: nil},
		{name: "whitespace", response: []byte(" \r\n")},
		{name: "too long", response: []byte("100000000")},
		{name: "prefix", response: []byte("0x1")},
		{name: "sign", response: []byte("+1")},
		{name: "internal whitespace", response: []byte("0000 001")},
		{name: "nul", response: []byte{'0', 0}},
		{name: "non ASCII", response: []byte{0xc2, 0xa0, '1'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseEnhancedCOMReadResponse(test.response); err == nil ||
				!errors.Is(err, errEnhancedCOMInvalidResponse) || !strings.Contains(err.Error(), "raw bytes") {
				t.Fatalf("response %q was accepted", test.response)
			}
		})
	}
}

func TestParseEnhancedCOMReadResponsePreservesRawDiagnosticBytes(t *testing.T) {
	_, err := parseEnhancedCOMReadResponse([]byte("x0 ??\r\n"))
	if err == nil || !strings.Contains(err.Error(), "78 30 20 3F 3F 0D 0A") {
		t.Fatalf("error = %v, want raw response bytes", err)
	}
}

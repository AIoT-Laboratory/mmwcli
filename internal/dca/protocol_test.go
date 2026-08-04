package dca

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBuildRequestAndParseResponse(t *testing.T) {
	request, err := BuildRequest(CommandConfigureFPGA, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x5A, 0xA5, 0x03, 0x00, 0x03, 0x00, 1, 2, 3, 0xAA, 0xEE}
	if !bytes.Equal(request, want) {
		t.Fatalf("request = % X, want % X", request, want)
	}
	if _, err := BuildRequest(CommandConfigureFPGA, make([]byte, MaximumPayloadSize+1)); err == nil {
		t.Fatal("BuildRequest accepted an oversized payload")
	}

	frame := testResponse(CommandReadFPGAVersion, 0x4402)
	response, err := ParseResponse(frame)
	if err != nil {
		t.Fatal(err)
	}
	if response.Command != CommandReadFPGAVersion || response.Status != 0x4402 || response.Source != nil {
		t.Fatalf("unexpected response: %+v", response)
	}
	for name, packet := range map[string][]byte{
		"short":      frame[:7],
		"extended":   append(append([]byte(nil), frame...), 0),
		"bad header": append([]byte{0, 0}, frame[2:]...),
		"bad footer": append(append([]byte(nil), frame[:6]...), 0, 0),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponse(packet); err == nil {
				t.Fatalf("ParseResponse accepted %s frame", name)
			}
		})
	}
}

func TestConfigurationPayloads(t *testing.T) {
	fpga, err := BuildFPGAConfig(DefaultFPGAConfig())
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 2, 1, 2, 3, 30}; !bytes.Equal(fpga, want) {
		t.Fatalf("FPGA config = % X, want % X", fpga, want)
	}
	invalid := DefaultFPGAConfig()
	invalid.DataFormat = 4
	if _, err := BuildFPGAConfig(invalid); err == nil {
		t.Fatal("BuildFPGAConfig accepted data format 4")
	}

	record, err := BuildRecordConfig(25)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0xBE, 0x05, 0x35, 0x0C, 0, 0}; !bytes.Equal(record, want) {
		t.Fatalf("record config = % X, want % X", record, want)
	}
	for _, delay := range []int{4, 501} {
		if _, err := BuildRecordConfig(delay); err == nil {
			t.Fatalf("BuildRecordConfig accepted delay %d", delay)
		}
	}
}

func TestValidateRawCaptureFPGAConfig(t *testing.T) {
	valid := DefaultFPGAConfig()
	if err := ValidateRawCaptureFPGAConfig(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*FPGAConfig)
	}{
		{name: "multi log", mutate: func(config *FPGAConfig) { config.LogMode = 2 }},
		{name: "four lane", mutate: func(config *FPGAConfig) { config.LVDSMode = 1 }},
		{name: "playback", mutate: func(config *FPGAConfig) { config.TransferMode = 2 }},
		{name: "SD", mutate: func(config *FPGAConfig) { config.CaptureMode = 1 }},
		{name: "14 bit", mutate: func(config *FPGAConfig) { config.DataFormat = 2 }},
		{name: "timer", mutate: func(config *FPGAConfig) { config.Timer = 31 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if err := ValidateRawCaptureFPGAConfig(config); err == nil {
				t.Fatalf("ValidateRawCaptureFPGAConfig accepted %+v", config)
			}
		})
	}
}

func TestParseDataPacketAndVersion(t *testing.T) {
	datagram := make([]byte, DataHeaderSize+3)
	binary.LittleEndian.PutUint32(datagram[0:4], 0x78563412)
	copy(datagram[4:10], []byte{1, 2, 3, 4, 5, 6})
	copy(datagram[10:], []byte{7, 8, 9})
	packet, err := ParseDataPacket(datagram)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Sequence != 0x78563412 || packet.ByteOffset != 0x060504030201 {
		t.Fatalf("unexpected data header: %+v", packet)
	}
	if !bytes.Equal(packet.Payload, []byte{7, 8, 9}) {
		t.Fatalf("payload = %v", packet.Payload)
	}
	if _, err := ParseDataPacket(make([]byte, DataHeaderSize)); err == nil {
		t.Fatal("ParseDataPacket accepted an empty payload")
	}
	if _, err := ParseDataPacket(make([]byte, DataHeaderSize+MaximumDataPayloadSize)); err != nil {
		t.Fatalf("ParseDataPacket rejected maximum payload: %v", err)
	}
	if _, err := ParseDataPacket(make([]byte, DataHeaderSize+MaximumDataPayloadSize+1)); err == nil {
		t.Fatal("ParseDataPacket accepted a payload above the DCA1000 maximum")
	}

	version := DecodeFPGAVersion(2 | 8<<7)
	if version.Major != 2 || version.Minor != 8 || version.Playback || version.String() != "2.8 (Record)" {
		t.Fatalf("unexpected FPGA version: %+v / %q", version, version.String())
	}
}

func TestSystemAsyncStatusClassificationUsesEnumValues(t *testing.T) {
	tests := []struct {
		status uint16
		fatal  bool
	}{
		{SystemStatusNoLVDSData, true},
		{SystemStatusNoHeader, true},
		{SystemStatusEEPROMFailure, true},
		{SystemStatusSDCardDetected, false},
		{SystemStatusSDCardRemoved, false},
		{SystemStatusSDCardFull, false},
		{SystemStatusModeConfigFailure, true},
		{SystemStatusDDRFull, true},
		{SystemStatusRecordCompleted, false},
		{SystemStatusLVDSBufferFull, true},
		{SystemStatusPlaybackCompleted, false},
		{SystemStatusPlaybackOutOfSequence, false},
		{0xffff, true},
	}
	for _, test := range tests {
		if got := IsFatalSystemStatus(test.status); got != test.fatal {
			t.Fatalf("IsFatalSystemStatus(%d) = %t, want %t", test.status, got, test.fatal)
		}
	}
}

func testResponse(command Command, status uint16) []byte {
	packet := make([]byte, ControlResponseSize)
	binary.LittleEndian.PutUint16(packet[0:2], ControlHeader)
	binary.LittleEndian.PutUint16(packet[2:4], uint16(command))
	binary.LittleEndian.PutUint16(packet[4:6], status)
	binary.LittleEndian.PutUint16(packet[6:8], ControlFooter)
	return packet
}

package debugcapture

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestBootstrapMMWaveLinkRequiresClient(t *testing.T) {
	if _, err := bootstrapMMWaveLinkForFamily(context.Background(), nil, debugFamilyIWR6843ES2); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %v", err)
	}
}

func TestBootstrapMMWaveLinkUsesFixedOrderingAndGoldenCommands(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	transport.queueFrames(validBootstrapFrames()...)
	client := mustMMWaveLinkClient(t, transport)

	diagnostics, err := bootstrapMMWaveLinkForFamily(context.Background(), client, debugFamilyIWR6843ES2)
	if err != nil {
		t.Fatal(err)
	}
	wantMSS := mmWaveLinkFirmwareVersion{
		HardwareVariant: 9,
		HardwareMajor:   7,
		HardwareMinor:   3,
		FirmwareMajor:   2,
		FirmwareMinor:   0,
		FirmwareBuild:   0,
		FirmwareDebug:   3,
		FirmwareYear:    24,
		FirmwareMonth:   8,
		FirmwareDay:     5,
		PatchMajor:      10,
		PatchMinor:      11,
		PatchYear:       25,
		PatchMonth:      1,
		PatchDay:        2,
		PatchBuildDebug: 0xa3,
	}
	if diagnostics.MSS != wantMSS {
		t.Fatalf("MSS diagnostics = %+v\nwant = %+v", diagnostics.MSS, wantMSS)
	}
	if diagnostics.RF.HardwareVariant != 1 ||
		diagnostics.RF.HardwareMajor != 0 ||
		diagnostics.RF.FirmwareMajor != 6 ||
		diagnostics.RF.FirmwareDebug != 5 {
		t.Fatalf("RF diagnostics = %+v", diagnostics.RF)
	}
	if diagnostics.RFPowerupStatus != mmWaveLinkRFPowerupStatusDone {
		t.Fatalf("RF power-up status = %d", diagnostics.RFPowerupStatus)
	}

	wantCommands := []string{
		"34122143C5811200000000000100277EE04004004931",
		"3412214305801200001000000100E76F0040040000DA",
		"3412214341041200002000000100ABDB200204005564",
	}
	if len(transport.commandWrites) != len(wantCommands) {
		t.Fatalf("command writes = %d", len(transport.commandWrites))
	}
	for index, command := range transport.commandWrites {
		if got := fmt.Sprintf("%X", command); got != wantCommands[index] {
			t.Fatalf("command %d = %s\nwant = %s", index, got, wantCommands[index])
		}
	}

	wantCalls := make([]string, 0, 28)
	wantCalls = append(wantCalls, bootstrapReceiveCalls(22)...)
	wantCalls = append(wantCalls, "write:"+wantCommands[0])
	wantCalls = append(wantCalls, bootstrapReceiveCalls(22)...)
	wantCalls = append(wantCalls, "write:"+wantCommands[1])
	wantCalls = append(wantCalls, bootstrapReceiveCalls(2)...)
	wantCalls = append(wantCalls, bootstrapReceiveCalls(22)...)
	wantCalls = append(wantCalls, "write:"+wantCommands[2])
	wantCalls = append(wantCalls, bootstrapReceiveCalls(22)...)
	if strings.Join(transport.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(transport.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if transport.missingDeadline {
		t.Fatal("bootstrap reached transport without bounded operation/event contexts")
	}
}

func TestBootstrapMMWaveLinkStopsAtEveryFailedGate(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func([][]byte)
		wantErr      string
		wantCommands int
	}{
		{
			name: "MSS event length",
			mutate: func(frames [][]byte) {
				frames[0] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassAsync,
					mmWaveLinkDeviceAsyncMessageID,
					0,
					0,
					[]mmWaveLinkSubblock{{id: mmWaveLinkMSSPowerupDoneSubblockID, data: make([]byte, 15)}},
				)
			},
			wantErr: "exactly 16",
		},
		{
			name: "MSS event extra sub-block",
			mutate: func(frames [][]byte) {
				frames[0] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassAsync,
					mmWaveLinkDeviceAsyncMessageID,
					0,
					0,
					[]mmWaveLinkSubblock{
						{id: mmWaveLinkMSSPowerupDoneSubblockID, data: make([]byte, 16)},
						{id: 4},
					},
				)
			},
			wantErr: "only sub-block",
		},
		{
			name: "MSS version shape",
			mutate: func(frames [][]byte) {
				frames[1] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassResponse,
					mmWaveLinkDeviceStatusGetMessageID,
					0,
					0,
					[]mmWaveLinkSubblock{{id: 0, data: make([]byte, 15)}},
				)
			},
			wantErr:      "one 16-byte",
			wantCommands: 1,
		},
		{
			name: "MSS rejects RF firmware release",
			mutate: func(frames [][]byte) {
				frames[1] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassResponse,
					mmWaveLinkDeviceStatusGetMessageID,
					0,
					0,
					[]mmWaveLinkSubblock{{id: 0, data: validRFVersion()}},
				)
			},
			wantErr:      "expected 2.0.0.3",
			wantCommands: 1,
		},
		{
			name: "RF start response payload",
			mutate: func(frames [][]byte) {
				frames[2] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassResponse,
					mmWaveLinkDevicePowerupMessageID,
					1,
					0,
					[]mmWaveLinkSubblock{{id: 0}},
				)
			},
			wantErr:      "header-only",
			wantCommands: 2,
		},
		{
			name: "RF event length",
			mutate: func(frames [][]byte) {
				frames[3] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassAsync,
					mmWaveLinkDeviceAsyncMessageID,
					0,
					0,
					[]mmWaveLinkSubblock{{id: mmWaveLinkRFPowerupDoneSubblockID, data: make([]byte, 15)}},
				)
			},
			wantErr:      "exactly 16",
			wantCommands: 2,
		},
		{
			name: "RF event status",
			mutate: func(frames [][]byte) {
				data := make([]byte, 16)
				binary.LittleEndian.PutUint32(data[:4], 1)
				frames[3] = clientTestInboundFrame(
					rhcpDirectionMSSToHost,
					rhcpMessageClassAsync,
					mmWaveLinkDeviceAsyncMessageID,
					0,
					0,
					[]mmWaveLinkSubblock{{id: mmWaveLinkRFPowerupDoneSubblockID, data: data}},
				)
			},
			wantErr:      "status",
			wantCommands: 2,
		},
		{
			name: "RF version shape",
			mutate: func(frames [][]byte) {
				frames[4] = clientTestInboundFrame(
					rhcpDirectionBSSToHost,
					rhcpMessageClassResponse,
					mmWaveLinkRFStatusGetMessageID,
					2,
					0,
					[]mmWaveLinkSubblock{{id: 0, data: make([]byte, 15)}},
				)
			},
			wantErr:      "one 16-byte",
			wantCommands: 3,
		},
		{
			name: "RF rejects MSS firmware release",
			mutate: func(frames [][]byte) {
				frames[4] = clientTestInboundFrame(
					rhcpDirectionBSSToHost,
					rhcpMessageClassResponse,
					mmWaveLinkRFStatusGetMessageID,
					2,
					0,
					[]mmWaveLinkSubblock{{id: 0, data: validMSSVersion()}},
				)
			},
			wantErr:      "expected 6.2.1.5",
			wantCommands: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frames := validBootstrapFrames()
			test.mutate(frames)
			transport := &fakeMMWaveLinkTransport{}
			transport.queueFrames(frames...)
			client := mustMMWaveLinkClient(t, transport)
			_, err := bootstrapMMWaveLinkForFamily(context.Background(), client, debugFamilyIWR6843ES2)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("bootstrap error = %v, want text %q", err, test.wantErr)
			}
			if len(transport.commandWrites) != test.wantCommands {
				t.Fatalf("commands after failed gate = %d, want %d", len(transport.commandWrites), test.wantCommands)
			}
		})
	}
}

func validBootstrapFrames() [][]byte {
	mssPowerupData := make([]byte, 16)
	for index := range mssPowerupData {
		mssPowerupData[index] = byte(0xa0 + index)
	}
	rfPowerupData := make([]byte, 16)
	binary.LittleEndian.PutUint32(rfPowerupData[:4], mmWaveLinkRFPowerupStatusDone)
	return [][]byte{
		clientTestInboundFrame(
			rhcpDirectionMSSToHost,
			rhcpMessageClassAsync,
			mmWaveLinkDeviceAsyncMessageID,
			0,
			0,
			[]mmWaveLinkSubblock{{id: mmWaveLinkMSSPowerupDoneSubblockID, data: mssPowerupData}},
		),
		clientTestInboundFrame(
			rhcpDirectionMSSToHost,
			rhcpMessageClassResponse,
			mmWaveLinkDeviceStatusGetMessageID,
			0,
			0,
			[]mmWaveLinkSubblock{{id: mmWaveLinkVersionSubblockID, data: validMSSVersion()}},
		),
		clientTestInboundFrame(
			rhcpDirectionMSSToHost,
			rhcpMessageClassResponse,
			mmWaveLinkDevicePowerupMessageID,
			1,
			0,
			nil,
		),
		clientTestInboundFrame(
			rhcpDirectionMSSToHost,
			rhcpMessageClassAsync,
			mmWaveLinkDeviceAsyncMessageID,
			0,
			0,
			[]mmWaveLinkSubblock{{id: mmWaveLinkRFPowerupDoneSubblockID, data: rfPowerupData}},
		),
		clientTestInboundFrame(
			rhcpDirectionBSSToHost,
			rhcpMessageClassResponse,
			mmWaveLinkRFStatusGetMessageID,
			2,
			0,
			[]mmWaveLinkSubblock{{id: mmWaveLinkVersionSubblockID, data: validRFVersion()}},
		),
	}
}

func validMSSVersion() []byte {
	return []byte{9, 7, 3, 2, 0, 0, 3, 24, 8, 5, 10, 11, 25, 1, 2, 0xa3}
}

func validRFVersion() []byte {
	return []byte{1, 0, 9, 6, 2, 1, 5, 23, 12, 31, 99, 88, 22, 6, 7, 0xf4}
}

func bootstrapReceiveCalls(remainderLength int) []string {
	return []string{
		"wait:true",
		"write:78566587FFFFFFFFFFFFFFFFFFFFFFFF",
		"wait:false",
		"read:16",
		fmt.Sprintf("read:%d", remainderLength),
	}
}

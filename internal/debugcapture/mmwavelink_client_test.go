package debugcapture

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMMWaveLinkClientMatchesGoldenTransactionOrdering(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	transport.queueFrames(littleEndianWords(
		0xdcba, 0xabcd,
		0x8016, 0x000e, 0x400c, 0x0000, 0x0000, 0x3fcf,
		0x0544,
	))
	client := mustMMWaveLinkClient(t, transport)
	client.sequence = 4

	response, err := client.execute(context.Background(), mmWaveLinkCommand{
		direction: rhcpDirectionHostToMSS,
		messageID: 0x200,
		subblocks: []mmWaveLinkSubblock{{id: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.messageID != 0x200 || response.sequence != 4 {
		t.Fatalf("response = %+v", response)
	}

	want := []string{
		"write:3412214305801200004000000100E73F00400400DADE",
		"wait:true",
		"write:78566587FFFFFFFFFFFFFFFFFFFFFFFF",
		"wait:false",
		"read:16",
		"read:2",
	}
	if strings.Join(transport.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(transport.calls, "\n"), strings.Join(want, "\n"))
	}
	if transport.missingDeadline {
		t.Fatal("transaction reached transport without a bounded context")
	}
}

func TestMMWaveLinkClientQueuesAsyncBeforeResponse(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	transport.queueFrames(
		clientTestInboundFrame(rhcpDirectionBSSToHost, rhcpMessageClassAsync, mmWaveLinkRFAsyncMessageID, 9, 0, []mmWaveLinkSubblock{{id: 0}}),
		clientTestInboundFrame(rhcpDirectionBSSToHost, rhcpMessageClassResponse, 0x11, 0, 0, nil),
	)
	client := mustMMWaveLinkClient(t, transport)
	if _, err := client.execute(context.Background(), mmWaveLinkCommand{
		direction: rhcpDirectionHostToBSS,
		messageID: 0x11,
		subblocks: []mmWaveLinkSubblock{{id: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	callsBeforeWait := len(transport.calls)
	event, err := client.waitEvent(context.Background(), mmWaveLinkRFAsyncMessageID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if event.messageID != mmWaveLinkRFAsyncMessageID || len(event.subblocks) != 1 {
		t.Fatalf("event = %+v", event)
	}
	if len(transport.calls) != callsBeforeWait {
		t.Fatalf("queued event triggered transport I/O: %v", transport.calls[callsBeforeWait:])
	}
}

func TestMMWaveLinkClientNeverRetriesUnknownFailures(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeMMWaveLinkTransport)
		wantIs    error
	}{
		{
			name: "NACK",
			configure: func(transport *fakeMMWaveLinkTransport) {
				transport.queueFrames(clientTestInboundFrame(
					rhcpDirectionBSSToHost,
					rhcpMessageClassNACK,
					0x11,
					0,
					0,
					nil,
				))
			},
			wantIs: errMMWaveLinkNACK,
		},
		{
			name: "write",
			configure: func(transport *fakeMMWaveLinkTransport) {
				transport.writeErrorAt = 1
				transport.writeError = errors.New("write failed")
			},
		},
		{
			name: "read",
			configure: func(transport *fakeMMWaveLinkTransport) {
				transport.readErrorAt = 1
				transport.readError = errors.New("read failed")
			},
		},
		{
			name: "context",
			configure: func(transport *fakeMMWaveLinkTransport) {
				transport.waitErrorAt = 1
				transport.waitError = context.Canceled
			},
			wantIs: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeMMWaveLinkTransport{}
			test.configure(transport)
			client := mustMMWaveLinkClient(t, transport)
			command := mmWaveLinkCommand{
				direction: rhcpDirectionHostToBSS,
				messageID: 0x11,
				subblocks: []mmWaveLinkSubblock{{id: 0}},
			}
			_, err := client.execute(context.Background(), command)
			if !errors.Is(err, errMMWaveLinkClientPoisoned) {
				t.Fatalf("first error = %v", err)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("first error = %v, want errors.Is(%v)", err, test.wantIs)
			}
			if len(transport.commandWrites) != 1 {
				t.Fatalf("command writes = %d, want 1", len(transport.commandWrites))
			}
			callsBeforeRetry := len(transport.calls)
			_, err = client.execute(context.Background(), command)
			if !errors.Is(err, errMMWaveLinkClientPoisoned) {
				t.Fatalf("second error = %v", err)
			}
			if len(transport.calls) != callsBeforeRetry {
				t.Fatalf("poisoned client retried I/O: %v", transport.calls[callsBeforeRetry:])
			}
		})
	}
}

func TestMMWaveLinkStatusErrorKeepsClientUsable(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	transport.queueFrames(
		clientTestInboundFrame(
			rhcpDirectionBSSToHost,
			rhcpMessageClassResponse,
			mmWaveLinkRFResponseErrorMessageID,
			0,
			0,
			[]mmWaveLinkSubblock{{id: 0, data: []byte{0x34, 0x12, 0x78, 0x56}}},
		),
		clientTestInboundFrame(rhcpDirectionBSSToHost, rhcpMessageClassResponse, 0x11, 1, 0, nil),
	)
	client := mustMMWaveLinkClient(t, transport)
	command := mmWaveLinkCommand{
		direction: rhcpDirectionHostToBSS,
		messageID: 0x11,
		subblocks: []mmWaveLinkSubblock{{id: 0}},
	}
	_, err := client.execute(context.Background(), command)
	var status *mmWaveLinkStatusError
	if !errors.As(err, &status) {
		t.Fatalf("first error = %v", err)
	}
	if status.statusCode != 0x1234 || status.subblockID != 0x5678 {
		t.Fatalf("status = %+v", status)
	}
	if errors.Is(err, errMMWaveLinkClientPoisoned) {
		t.Fatalf("known status poisoned client: %v", err)
	}
	if _, err := client.execute(context.Background(), command); err != nil {
		t.Fatalf("second command: %v", err)
	}
	if len(transport.commandWrites) != 2 {
		t.Fatalf("command writes = %d", len(transport.commandWrites))
	}
	if got := binary.LittleEndian.Uint16(transport.commandWrites[0][8:10]) >> 12; got != 0 {
		t.Fatalf("first sequence = %d", got)
	}
	if got := binary.LittleEndian.Uint16(transport.commandWrites[1][8:10]) >> 12; got != 1 {
		t.Fatalf("second sequence = %d", got)
	}
}

func TestMMWaveLinkStatusErrorAcceptsExpectedMSSDirection(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	transport.queueFrames(clientTestInboundFrame(
		rhcpDirectionMSSToHost,
		rhcpMessageClassResponse,
		mmWaveLinkRFResponseErrorMessageID,
		0,
		0,
		[]mmWaveLinkSubblock{{id: 0, data: []byte{1, 0, 2, 0}}},
	))
	client := mustMMWaveLinkClient(t, transport)
	_, err := client.execute(context.Background(), mmWaveLinkCommand{
		direction: rhcpDirectionHostToMSS,
		messageID: 0x207,
	})
	var status *mmWaveLinkStatusError
	if !errors.As(err, &status) || errors.Is(err, errMMWaveLinkClientPoisoned) {
		t.Fatalf("error = %v", err)
	}
}

func TestMMWaveLinkClientPoisonsZeroStatusResponse(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	transport.queueFrames(clientTestInboundFrame(
		rhcpDirectionBSSToHost,
		rhcpMessageClassResponse,
		mmWaveLinkRFResponseErrorMessageID,
		0,
		0,
		[]mmWaveLinkSubblock{{id: 0, data: []byte{0, 0, 2, 0}}},
	))
	client := mustMMWaveLinkClient(t, transport)
	_, err := client.execute(context.Background(), mmWaveLinkCommand{
		direction: rhcpDirectionHostToBSS,
		messageID: 0x11,
	})
	if !errors.Is(err, errMMWaveLinkClientPoisoned) || !strings.Contains(err.Error(), "zero status") {
		t.Fatalf("error = %v", err)
	}
}

func TestMMWaveLinkClientRejectsChunkedCommandBeforeIO(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	client := mustMMWaveLinkClient(t, transport)
	_, err := client.execute(context.Background(), mmWaveLinkCommand{
		direction:       rhcpDirectionHostToBSS,
		messageID:       0x11,
		remainingChunks: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "chunked") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, errMMWaveLinkClientPoisoned) {
		t.Fatalf("local validation poisoned client: %v", err)
	}
	if len(transport.calls) != 0 || client.sequence != 0 {
		t.Fatalf("local validation reached transport or consumed sequence: calls=%v sequence=%d", transport.calls, client.sequence)
	}
}

func TestMMWaveLinkClientRejectsMismatchedResponses(t *testing.T) {
	tests := []struct {
		name      string
		direction rhcpDirection
		sequence  uint8
		chunks    uint16
		want      string
	}{
		{name: "direction", direction: rhcpDirectionMSSToHost, want: "direction"},
		{name: "sequence", direction: rhcpDirectionBSSToHost, sequence: 1, want: "sequence"},
		{name: "chunks", direction: rhcpDirectionBSSToHost, chunks: 1, want: "chunks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeMMWaveLinkTransport{}
			transport.queueFrames(clientTestInboundFrame(
				test.direction,
				rhcpMessageClassResponse,
				0x11,
				test.sequence,
				test.chunks,
				nil,
			))
			client := mustMMWaveLinkClient(t, transport)
			_, err := client.execute(context.Background(), mmWaveLinkCommand{
				direction: rhcpDirectionHostToBSS,
				messageID: 0x11,
			})
			if !errors.Is(err, errMMWaveLinkClientPoisoned) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMMWaveLinkClientBoundsDeclaredLengthBeforeAllocation(t *testing.T) {
	tests := []struct {
		name           string
		declaredLength uint16
		want           string
	}{
		{name: "below minimum", declaredLength: 12, want: "valid range"},
		{name: "above maximum", declaredLength: 254, want: "valid range"},
		{name: "odd", declaredLength: 15, want: "not even"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix := make([]byte, rhcpSyncLength+rhcpHeaderLength)
			binary.LittleEndian.PutUint16(prefix[0:2], rhcpDeviceToHostSyncWord1)
			binary.LittleEndian.PutUint16(prefix[2:4], rhcpDeviceToHostSyncWord2)
			binary.LittleEndian.PutUint16(prefix[6:8], test.declaredLength)
			transport := &fakeMMWaveLinkTransport{readChunks: [][]byte{prefix}}
			client := mustMMWaveLinkClient(t, transport)
			_, err := client.execute(context.Background(), mmWaveLinkCommand{
				direction: rhcpDirectionHostToBSS,
				messageID: 0x11,
			})
			if !errors.Is(err, errMMWaveLinkClientPoisoned) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			if transport.readCalls != 1 {
				t.Fatalf("read calls = %d, invalid length reached payload read", transport.readCalls)
			}
		})
	}
}

func TestMMWaveLinkClientBoundsAsyncQueue(t *testing.T) {
	transport := &fakeMMWaveLinkTransport{}
	for index := 0; index <= mmWaveLinkAsyncQueueLimit; index++ {
		transport.queueFrames(clientTestInboundFrame(
			rhcpDirectionBSSToHost,
			rhcpMessageClassAsync,
			mmWaveLinkRFAsyncMessageID,
			uint8(index&0x0f),
			0,
			[]mmWaveLinkSubblock{{id: 0}},
		))
	}
	client := mustMMWaveLinkClient(t, transport)
	_, err := client.waitEvent(context.Background(), mmWaveLinkRFAsyncMessage1ID, 0)
	if !errors.Is(err, errMMWaveLinkClientPoisoned) || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("error = %v", err)
	}
	if len(client.asyncEvents) != mmWaveLinkAsyncQueueLimit {
		t.Fatalf("queued events = %d", len(client.asyncEvents))
	}
	callsBeforeRetry := len(transport.calls)
	if _, err := client.waitEvent(context.Background(), mmWaveLinkRFAsyncMessageID, 0); !errors.Is(err, errMMWaveLinkClientPoisoned) {
		t.Fatalf("second error = %v", err)
	}
	if len(transport.calls) != callsBeforeRetry {
		t.Fatalf("poisoned queue reached transport: %v", transport.calls[callsBeforeRetry:])
	}
}

func TestMMWaveLinkClientPoisonsOnFatalAsyncFaults(t *testing.T) {
	tests := []struct {
		name      string
		direction rhcpDirection
		messageID uint16
		subblock  uint16
	}{
		{name: "RF CPU", direction: rhcpDirectionBSSToHost, messageID: mmWaveLinkRFAsyncMessageID, subblock: 0x02},
		{name: "MSS ESM", direction: rhcpDirectionMSSToHost, messageID: mmWaveLinkDeviceAsyncMessageID, subblock: 0x03},
		{name: "MSS boot", direction: rhcpDirectionMSSToHost, messageID: mmWaveLinkDeviceAsyncMessageID, subblock: 0x05},
		{name: "internal mismatch", direction: rhcpDirectionMSSToHost, messageID: mmWaveLinkInternalAsyncMessageID, subblock: 0x00},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeMMWaveLinkTransport{}
			transport.queueFrames(clientTestInboundFrame(
				test.direction,
				rhcpMessageClassAsync,
				test.messageID,
				0,
				0,
				[]mmWaveLinkSubblock{{id: test.subblock}},
			))
			client := mustMMWaveLinkClient(t, transport)
			_, err := client.waitEvent(context.Background(), 0x81, 0)
			var fault *mmWaveLinkAsyncFaultError
			if !errors.Is(err, errMMWaveLinkClientPoisoned) || !errors.As(err, &fault) {
				t.Fatalf("error = %v", err)
			}
			if fault.messageID != test.messageID || fault.subblockID != test.subblock {
				t.Fatalf("fault = %+v", fault)
			}
		})
	}
}

func TestMMWaveLinkClientRejectsUnsupportedOrEmptyAsyncEvents(t *testing.T) {
	tests := []struct {
		name      string
		messageID uint16
		subblocks []mmWaveLinkSubblock
		want      string
	}{
		{name: "unsupported", messageID: 0x082, subblocks: []mmWaveLinkSubblock{{id: 0}}, want: "unsupported"},
		{name: "empty", messageID: mmWaveLinkRFAsyncMessageID, want: "no sub-blocks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeMMWaveLinkTransport{}
			transport.queueFrames(clientTestInboundFrame(
				rhcpDirectionBSSToHost,
				rhcpMessageClassAsync,
				test.messageID,
				0,
				0,
				test.subblocks,
			))
			client := mustMMWaveLinkClient(t, transport)
			_, err := client.waitEvent(context.Background(), mmWaveLinkRFAsyncMessageID, 0)
			if !errors.Is(err, errMMWaveLinkClientPoisoned) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func mustMMWaveLinkClient(t *testing.T, transport mmWaveLinkTransport) *mmWaveLinkClient {
	t.Helper()
	client, err := newMMWaveLinkClient(transport)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func clientTestInboundFrame(
	direction rhcpDirection,
	messageClass rhcpMessageClass,
	messageID uint16,
	sequence uint8,
	remainingChunks uint16,
	subblocks []mmWaveLinkSubblock,
) []byte {
	payloadLength := 0
	for _, subblock := range subblocks {
		payloadLength += rhcpSubblockHeaderLength + len(subblock.data)
	}
	paddingLength := (rhcpProtocolAlignment - ((rhcpHeaderLength + payloadLength) % rhcpProtocolAlignment)) % rhcpProtocolAlignment
	declaredLength := rhcpHeaderLength + payloadLength + paddingLength + rhcpCRC16Length
	frame := make([]byte, rhcpSyncLength+declaredLength)
	binary.LittleEndian.PutUint16(frame[0:2], rhcpDeviceToHostSyncWord1)
	binary.LittleEndian.PutUint16(frame[2:4], rhcpDeviceToHostSyncWord2)
	binary.LittleEndian.PutUint16(frame[4:6], uint16(direction)|uint16(messageClass)<<4|messageID<<6)
	binary.LittleEndian.PutUint16(frame[6:8], uint16(declaredLength))
	binary.LittleEndian.PutUint16(frame[8:10], uint16(sequence)<<12|rhcpFlagACKMask)
	binary.LittleEndian.PutUint16(frame[10:12], remainingChunks)
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
	binary.LittleEndian.PutUint16(
		frame[len(frame)-rhcpCRC16Length:],
		rhcpCRC16CCITTFalse(frame[4:len(frame)-rhcpCRC16Length]),
	)
	return frame
}

type fakeMMWaveLinkTransport struct {
	calls           []string
	commandWrites   [][]byte
	readChunks      [][]byte
	writeCalls      int
	readCalls       int
	waitCalls       int
	writeErrorAt    int
	readErrorAt     int
	waitErrorAt     int
	writeError      error
	readError       error
	waitError       error
	missingDeadline bool
}

func (transport *fakeMMWaveLinkTransport) SPIWrite(ctx context.Context, data []byte) error {
	transport.recordDeadline(ctx)
	transport.writeCalls++
	transport.calls = append(transport.calls, fmt.Sprintf("write:%X", data))
	if !isMMWaveLinkCNYS(data) {
		transport.commandWrites = append(transport.commandWrites, append([]byte(nil), data...))
	}
	if transport.writeCalls == transport.writeErrorAt {
		return transport.writeError
	}
	return nil
}

func (transport *fakeMMWaveLinkTransport) SPIRead(ctx context.Context, data []byte) error {
	transport.recordDeadline(ctx)
	transport.readCalls++
	transport.calls = append(transport.calls, fmt.Sprintf("read:%d", len(data)))
	if transport.readCalls == transport.readErrorAt {
		return transport.readError
	}
	if len(transport.readChunks) == 0 {
		return errors.New("unexpected SPI read")
	}
	chunk := transport.readChunks[0]
	transport.readChunks = transport.readChunks[1:]
	if len(chunk) != len(data) {
		return fmt.Errorf("SPI read length is %d; queued chunk is %d", len(data), len(chunk))
	}
	copy(data, chunk)
	return nil
}

func (transport *fakeMMWaveLinkTransport) WaitIRQ(ctx context.Context, asserted bool) error {
	transport.recordDeadline(ctx)
	transport.waitCalls++
	transport.calls = append(transport.calls, fmt.Sprintf("wait:%t", asserted))
	if transport.waitCalls == transport.waitErrorAt {
		return transport.waitError
	}
	return nil
}

func (transport *fakeMMWaveLinkTransport) queueFrames(frames ...[]byte) {
	for _, frame := range frames {
		transport.readChunks = append(
			transport.readChunks,
			append([]byte(nil), frame[:rhcpSyncLength+rhcpHeaderLength]...),
			append([]byte(nil), frame[rhcpSyncLength+rhcpHeaderLength:]...),
		)
	}
}

func (transport *fakeMMWaveLinkTransport) recordDeadline(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok {
		transport.missingDeadline = true
	}
}

func isMMWaveLinkCNYS(data []byte) bool {
	if len(data) != rhcpSyncLength+rhcpHeaderLength ||
		binary.LittleEndian.Uint16(data[0:2]) != 0x5678 ||
		binary.LittleEndian.Uint16(data[2:4]) != 0x8765 {
		return false
	}
	return bytes.Equal(data[4:], bytes.Repeat([]byte{rhcpProtocolDummyByte}, 12))
}

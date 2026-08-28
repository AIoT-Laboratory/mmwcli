package iwr6843

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	mmWaveLinkOperationTimeout = time.Second
	mmWaveLinkEventTimeout     = 5 * time.Second
	mmWaveLinkAsyncQueueLimit  = 32

	mmWaveLinkRFResponseErrorMessageID = 0x000
	mmWaveLinkRFAsyncMessageID         = 0x080
	mmWaveLinkRFAsyncMessage1ID        = 0x081
	mmWaveLinkDeviceAsyncMessageID     = 0x280
	mmWaveLinkInternalAsyncMessageID   = 0x380
)

var (
	errMMWaveLinkClientPoisoned = errors.New("mmWaveLink client state is unknown")
	errMMWaveLinkNACK           = errors.New("mmWaveLink command was rejected with NACK")
)

type mmWaveLinkTransport interface {
	SPIWrite(context.Context, []byte) error
	SPIRead(context.Context, []byte) error
	WaitIRQ(context.Context, bool) error
}

type mmWaveLinkClient struct {
	transport   mmWaveLinkTransport
	gate        chan struct{}
	sequence    uint8
	asyncEvents []mmWaveLinkMessage
	poisonCause error
}

type mmWaveLinkStatusError struct {
	statusCode uint16
	// subblockID is rlErrorResp_t.sbcID: the RHCP unique sub-block ID,
	// not the command-local five-bit sub-block number.
	subblockID uint16
}

func (status *mmWaveLinkStatusError) Error() string {
	return fmt.Sprintf("mmWaveLink status %#04x for unique sub-block %#04x", status.statusCode, status.subblockID)
}

type mmWaveLinkAsyncFaultError struct {
	messageID  uint16
	subblockID uint16
}

func (fault *mmWaveLinkAsyncFaultError) Error() string {
	return fmt.Sprintf("fatal mmWaveLink async fault message %#03x sub-block %#02x", fault.messageID, fault.subblockID)
}

func newMMWaveLinkClient(transport mmWaveLinkTransport) (*mmWaveLinkClient, error) {
	if transport == nil {
		return nil, errors.New("mmWaveLink transport is required")
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &mmWaveLinkClient{transport: transport, gate: gate}, nil
}

func (client *mmWaveLinkClient) execute(
	ctx context.Context,
	command mmWaveLinkCommand,
) (mmWaveLinkMessage, error) {
	opCtx, cancel := boundedMMWaveLinkContext(ctx, mmWaveLinkOperationTimeout)
	defer cancel()
	if err := client.acquire(opCtx); err != nil {
		return mmWaveLinkMessage{}, err
	}
	defer client.release()
	return client.executeLocked(opCtx, command)
}

func (client *mmWaveLinkClient) waitEvent(
	ctx context.Context,
	messageID uint16,
	subblockID uint16,
) (mmWaveLinkMessage, error) {
	eventCtx, cancel := boundedMMWaveLinkContext(ctx, mmWaveLinkEventTimeout)
	defer cancel()
	if err := client.acquire(eventCtx); err != nil {
		return mmWaveLinkMessage{}, err
	}
	defer client.release()
	return client.waitEventLocked(eventCtx, messageID, subblockID)
}

func (client *mmWaveLinkClient) executeLocked(
	ctx context.Context,
	command mmWaveLinkCommand,
) (mmWaveLinkMessage, error) {
	if err := client.checkUsableLocked(); err != nil {
		return mmWaveLinkMessage{}, err
	}
	if command.remainingChunks != 0 {
		return mmWaveLinkMessage{}, fmt.Errorf("chunked mmWaveLink commands are not supported")
	}
	command.sequence = client.sequence
	frame, err := encodeMMWaveLinkCommand(command)
	if err != nil {
		return mmWaveLinkMessage{}, err
	}
	sequence := client.sequence
	client.sequence = (client.sequence + 1) & 0x0f
	if err := client.transport.SPIWrite(ctx, frame); err != nil {
		return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf("write mmWaveLink command: %w", err))
	}

	for {
		message, err := client.receiveLocked(ctx)
		if err != nil {
			return mmWaveLinkMessage{}, client.poisonLocked(err)
		}
		switch message.messageClass {
		case rhcpMessageClassAsync:
			if err := client.validateAsyncLocked(message); err != nil {
				return mmWaveLinkMessage{}, client.poisonLocked(err)
			}
			if err := client.enqueueAsyncLocked(message); err != nil {
				return mmWaveLinkMessage{}, client.poisonLocked(err)
			}
			continue
		case rhcpMessageClassNACK:
			return mmWaveLinkMessage{}, client.poisonLocked(errMMWaveLinkNACK)
		case rhcpMessageClassResponse:
			return client.validateResponseLocked(command, sequence, message)
		default:
			return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf("unexpected mmWaveLink message class %d", message.messageClass))
		}
	}
}

func (client *mmWaveLinkClient) validateResponseLocked(
	command mmWaveLinkCommand,
	sequence uint8,
	response mmWaveLinkMessage,
) (mmWaveLinkMessage, error) {
	wantDirection := rhcpDirectionBSSToHost
	if command.direction == rhcpDirectionHostToMSS {
		wantDirection = rhcpDirectionMSSToHost
	}
	if response.direction != wantDirection {
		return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf(
			"mmWaveLink response direction is %d; expected %d",
			response.direction,
			wantDirection,
		))
	}
	if response.sequence != sequence {
		return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf(
			"mmWaveLink response sequence is %d; expected %d",
			response.sequence,
			sequence,
		))
	}
	if response.remainingChunks != 0 {
		return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf(
			"chunked mmWaveLink response has %d chunks remaining",
			response.remainingChunks,
		))
	}
	if response.messageID == command.messageID {
		return response, nil
	}
	if response.messageID != mmWaveLinkRFResponseErrorMessageID ||
		len(response.subblocks) != 1 ||
		response.subblocks[0].id != 0 ||
		len(response.subblocks[0].data) != 4 {
		return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf(
			"mmWaveLink response message ID is %#03x; expected %#03x",
			response.messageID,
			command.messageID,
		))
	}
	status := &mmWaveLinkStatusError{
		statusCode: binary.LittleEndian.Uint16(response.subblocks[0].data[0:2]),
		subblockID: binary.LittleEndian.Uint16(response.subblocks[0].data[2:4]),
	}
	if status.statusCode == 0 {
		return mmWaveLinkMessage{}, client.poisonLocked(errors.New("mmWaveLink error response has zero status"))
	}
	if !mmWaveLinkCommandContainsUniqueSubblock(command, status.subblockID) {
		return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf(
			"mmWaveLink error response unique sub-block %#04x does not belong to command %#03x",
			status.subblockID,
			command.messageID,
		))
	}
	return mmWaveLinkMessage{}, status
}

func mmWaveLinkCommandContainsUniqueSubblock(command mmWaveLinkCommand, uniqueSubblockID uint16) bool {
	for _, subblock := range command.subblocks {
		if command.messageID*rhcpMaxSubblocks+subblock.id == uniqueSubblockID {
			return true
		}
	}
	return false
}

func (client *mmWaveLinkClient) waitEventLocked(
	ctx context.Context,
	messageID uint16,
	subblockID uint16,
) (mmWaveLinkMessage, error) {
	if err := client.checkUsableLocked(); err != nil {
		return mmWaveLinkMessage{}, err
	}
	if event, ok := client.takeQueuedEventLocked(messageID, subblockID); ok {
		return event, nil
	}
	for {
		message, err := client.receiveLocked(ctx)
		if err != nil {
			return mmWaveLinkMessage{}, client.poisonLocked(err)
		}
		if message.messageClass != rhcpMessageClassAsync {
			return mmWaveLinkMessage{}, client.poisonLocked(fmt.Errorf(
				"received mmWaveLink class %d while waiting for an async event",
				message.messageClass,
			))
		}
		if err := client.validateAsyncLocked(message); err != nil {
			return mmWaveLinkMessage{}, client.poisonLocked(err)
		}
		if mmWaveLinkEventMatches(message, messageID, subblockID) {
			return message, nil
		}
		if err := client.enqueueAsyncLocked(message); err != nil {
			return mmWaveLinkMessage{}, client.poisonLocked(err)
		}
	}
}

func (client *mmWaveLinkClient) receiveLocked(ctx context.Context) (mmWaveLinkMessage, error) {
	var cnys [rhcpSyncLength + rhcpHeaderLength]byte
	binary.LittleEndian.PutUint16(cnys[0:2], 0x5678)
	binary.LittleEndian.PutUint16(cnys[2:4], 0x8765)
	for index := rhcpSyncLength; index < len(cnys); index++ {
		cnys[index] = rhcpProtocolDummyByte
	}
	if err := client.transport.WaitIRQ(ctx, true); err != nil {
		return mmWaveLinkMessage{}, fmt.Errorf("wait for asserted mmWaveLink IRQ: %w", err)
	}
	if err := client.transport.SPIWrite(ctx, cnys[:]); err != nil {
		return mmWaveLinkMessage{}, fmt.Errorf("write mmWaveLink CNYS: %w", err)
	}
	if err := client.transport.WaitIRQ(ctx, false); err != nil {
		return mmWaveLinkMessage{}, fmt.Errorf("wait for cleared mmWaveLink IRQ: %w", err)
	}

	var prefix [rhcpSyncLength + rhcpHeaderLength]byte
	if err := client.transport.SPIRead(ctx, prefix[:]); err != nil {
		return mmWaveLinkMessage{}, fmt.Errorf("read mmWaveLink sync and header: %w", err)
	}
	if binary.LittleEndian.Uint16(prefix[0:2]) != rhcpDeviceToHostSyncWord1 ||
		binary.LittleEndian.Uint16(prefix[2:4]) != rhcpDeviceToHostSyncWord2 {
		return mmWaveLinkMessage{}, fmt.Errorf("invalid RHCP device-to-host sync % x", prefix[:rhcpSyncLength])
	}
	declaredLength := int(binary.LittleEndian.Uint16(prefix[6:8]))
	minimumLength := rhcpHeaderLength + rhcpCRC16Length
	maximumLength := rhcpMaxMessageLength - rhcpSyncLength
	if declaredLength < minimumLength || declaredLength > maximumLength {
		return mmWaveLinkMessage{}, fmt.Errorf(
			"RHCP declared length is %d; valid range is %d..%d",
			declaredLength,
			minimumLength,
			maximumLength,
		)
	}
	if declaredLength%2 != 0 {
		return mmWaveLinkMessage{}, fmt.Errorf("RHCP declared length %d is not even", declaredLength)
	}
	wantHeaderChecksum := rhcpHeaderChecksum(prefix[4:14])
	gotHeaderChecksum := binary.LittleEndian.Uint16(prefix[14:16])
	if gotHeaderChecksum != wantHeaderChecksum {
		return mmWaveLinkMessage{}, fmt.Errorf(
			"RHCP header checksum is %#04x; expected %#04x",
			gotHeaderChecksum,
			wantHeaderChecksum,
		)
	}

	remainderLength := declaredLength - rhcpHeaderLength
	frame := make([]byte, len(prefix)+remainderLength)
	copy(frame, prefix[:])
	if err := client.transport.SPIRead(ctx, frame[len(prefix):]); err != nil {
		return mmWaveLinkMessage{}, fmt.Errorf("read mmWaveLink payload and CRC: %w", err)
	}
	message, err := decodeMMWaveLinkMessage(frame)
	if err != nil {
		return mmWaveLinkMessage{}, fmt.Errorf("decode mmWaveLink message: %w", err)
	}
	return message, nil
}

func (client *mmWaveLinkClient) validateAsyncLocked(message mmWaveLinkMessage) error {
	if message.remainingChunks != 0 {
		return fmt.Errorf("chunked mmWaveLink async event has %d chunks remaining", message.remainingChunks)
	}
	if len(message.subblocks) == 0 {
		return errors.New("mmWaveLink async event has no sub-blocks")
	}
	switch message.messageID {
	case mmWaveLinkRFAsyncMessageID, mmWaveLinkRFAsyncMessage1ID:
		if message.direction != rhcpDirectionBSSToHost {
			return fmt.Errorf("RF async event direction is %d; expected %d", message.direction, rhcpDirectionBSSToHost)
		}
	case mmWaveLinkDeviceAsyncMessageID, mmWaveLinkInternalAsyncMessageID:
		if message.direction != rhcpDirectionMSSToHost {
			return fmt.Errorf("MSS async event direction is %d; expected %d", message.direction, rhcpDirectionMSSToHost)
		}
	default:
		return fmt.Errorf("unsupported mmWaveLink async message ID %#03x", message.messageID)
	}
	if fault := fatalMMWaveLinkAsyncFault(message); fault != nil {
		return fault
	}
	return nil
}

func fatalMMWaveLinkAsyncFault(message mmWaveLinkMessage) error {
	for _, subblock := range message.subblocks {
		fatal := false
		switch message.messageID {
		case mmWaveLinkRFAsyncMessageID:
			fatal = subblock.id == 0x02 || subblock.id == 0x03 || subblock.id == 0x10
		case mmWaveLinkDeviceAsyncMessageID:
			fatal = subblock.id == 0x02 || subblock.id == 0x03 || subblock.id == 0x05 || subblock.id == 0x08 || subblock.id == 0x09
		case mmWaveLinkInternalAsyncMessageID:
			fatal = subblock.id == 0x00 || subblock.id == 0x01
		}
		if fatal {
			return &mmWaveLinkAsyncFaultError{messageID: message.messageID, subblockID: subblock.id}
		}
	}
	return nil
}

func (client *mmWaveLinkClient) enqueueAsyncLocked(message mmWaveLinkMessage) error {
	if len(client.asyncEvents) >= mmWaveLinkAsyncQueueLimit {
		return fmt.Errorf("mmWaveLink async event queue reached its %d-event limit", mmWaveLinkAsyncQueueLimit)
	}
	client.asyncEvents = append(client.asyncEvents, message)
	return nil
}

func (client *mmWaveLinkClient) takeQueuedEventLocked(
	messageID uint16,
	subblockID uint16,
) (mmWaveLinkMessage, bool) {
	for index, event := range client.asyncEvents {
		if !mmWaveLinkEventMatches(event, messageID, subblockID) {
			continue
		}
		copy(client.asyncEvents[index:], client.asyncEvents[index+1:])
		last := len(client.asyncEvents) - 1
		client.asyncEvents[last] = mmWaveLinkMessage{}
		client.asyncEvents = client.asyncEvents[:last]
		return event, true
	}
	return mmWaveLinkMessage{}, false
}

func mmWaveLinkEventMatches(message mmWaveLinkMessage, messageID, subblockID uint16) bool {
	if message.messageID != messageID {
		return false
	}
	for _, subblock := range message.subblocks {
		if subblock.id == subblockID {
			return true
		}
	}
	return false
}

func (client *mmWaveLinkClient) acquire(ctx context.Context) error {
	if client == nil || client.gate == nil {
		return errors.New("mmWaveLink client is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.gate:
		return nil
	}
}

func (client *mmWaveLinkClient) release() {
	client.gate <- struct{}{}
}

func (client *mmWaveLinkClient) checkUsableLocked() error {
	if client.poisonCause == nil {
		return nil
	}
	return fmt.Errorf("%w: previous failure: %w", errMMWaveLinkClientPoisoned, client.poisonCause)
}

func (client *mmWaveLinkClient) poisonLocked(cause error) error {
	if client.poisonCause == nil {
		client.poisonCause = cause
	}
	return fmt.Errorf("%w: %w", errMMWaveLinkClientPoisoned, client.poisonCause)
}

func boundedMMWaveLinkContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

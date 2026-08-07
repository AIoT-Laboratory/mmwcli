package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errEnhancedCOMClosed      = errors.New("Enhanced COM client is closed")
	errEnhancedCOMUnusable    = errors.New("Enhanced COM client cannot continue after an unknown result")
	errEnhancedCOMReadTimeout = errors.New("Enhanced COM read response timed out")
)

const (
	enhancedCOMReadSettle          = 100 * time.Millisecond
	enhancedCOMReadQuiet           = 20 * time.Millisecond
	enhancedCOMFirstWakeWait       = 400 * time.Millisecond
	enhancedCOMProbeWakeWait       = 100 * time.Millisecond
	enhancedCOMMaximumResponseSize = 64
)

type enhancedCOMTransport interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	PurgeInput() error
}

// enhancedCOMUnknownResultError marks a command that may have reached the
// device without a complete, unambiguous host-side result. The client is not
// reusable after this error.
type enhancedCOMUnknownResultError struct {
	operation string
	written   int
	expected  int
	cause     error
}

func (err *enhancedCOMUnknownResultError) Error() string {
	return fmt.Sprintf(
		"Enhanced COM %s result is unknown after writing %d of %d bytes: %v",
		err.operation,
		err.written,
		err.expected,
		err.cause,
	)
}

func (err *enhancedCOMUnknownResultError) Unwrap() error { return err.cause }

// enhancedCOMClient serializes the ASCII debug monitor protocol over one
// already-open serial transport. A successful write means only that the host
// accepted the complete command; the protocol provides no write ACK.
type enhancedCOMClient struct {
	transport        enhancedCOMTransport
	operationTimeout time.Duration
	wait             func(context.Context, time.Duration) error
	lifecycle        context.Context
	lifecycleCancel  context.CancelFunc
	mu               sync.Mutex
	unusable         bool
	closed           atomic.Bool
	closeOnce        sync.Once
	closeErr         error
}

func newEnhancedCOMClient(transport enhancedCOMTransport, operationTimeout time.Duration) (*enhancedCOMClient, error) {
	if transport == nil {
		return nil, errors.New("Enhanced COM transport is nil")
	}
	if operationTimeout <= 0 {
		return nil, errors.New("Enhanced COM operation timeout must be positive")
	}
	lifecycle, lifecycleCancel := context.WithCancel(context.Background())
	return &enhancedCOMClient{
		transport:        transport,
		operationTimeout: operationTimeout,
		wait:             waitContext,
		lifecycle:        lifecycle,
		lifecycleCancel:  lifecycleCancel,
	}, nil
}

func (client *enhancedCOMClient) close() error {
	if client == nil {
		return nil
	}
	client.closed.Store(true)
	client.lifecycleCancel()
	client.closeOnce.Do(func() {
		client.closeErr = client.transport.Close()
	})
	return client.closeErr
}

// initialize reproduces Studio's fixed three-wake connection sequence. A
// TI-compatible one-to-eight-digit hexadecimal reply establishes the SOP2
// monitor exchange; the connection must still gate TOPRCM part identity before
// any target write.
func (client *enhancedCOMClient) initialize(ctx context.Context) (uint32, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	ctx, deadline, finish, err := client.beginLocked(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()

	wake := encodeEnhancedCOMWake()
	if err := client.writeLocked(ctx, deadline, "initial wake", wake); err != nil {
		return 0, err
	}
	if err := client.wait(ctx, enhancedCOMFirstWakeWait); err != nil {
		return 0, client.failSubmittedLocked("initial wake wait", len(wake), err)
	}
	status, err := client.probeLocked(ctx, deadline)
	if err != nil {
		return 0, err
	}
	if err := client.writeLocked(ctx, deadline, "final wake", wake); err != nil {
		return 0, err
	}
	return status, nil
}

// probe performs Studio's IsConnected exchange without the outer Init wakes.
// The fixed 115200 side of baud negotiation uses exactly this shorter form.
func (client *enhancedCOMClient) probe(ctx context.Context) (uint32, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	ctx, deadline, finish, err := client.beginLocked(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	return client.probeLocked(ctx, deadline)
}

func (client *enhancedCOMClient) probeLocked(ctx context.Context, deadline time.Time) (uint32, error) {
	wake := encodeEnhancedCOMWake()
	if err := client.writeLocked(ctx, deadline, "probe wake", wake); err != nil {
		return 0, err
	}
	if err := client.wait(ctx, enhancedCOMProbeWakeWait); err != nil {
		return 0, client.failSubmittedLocked("probe wake wait", len(wake), err)
	}
	return client.readRegisterLocked(ctx, deadline, 0xffffe2fc)
}

func (client *enhancedCOMClient) readRegister(ctx context.Context, address uint32) (uint32, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	ctx, deadline, finish, err := client.beginLocked(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	return client.readRegisterLocked(ctx, deadline, address)
}

func (client *enhancedCOMClient) readRegisterLocked(
	ctx context.Context,
	deadline time.Time,
	address uint32,
) (uint32, error) {
	if err := client.transport.PurgeInput(); err != nil {
		return 0, fmt.Errorf("purge stale Enhanced COM input: %w", err)
	}

	command := encodeEnhancedCOMRead(address)
	if err := client.writeLocked(ctx, deadline, fmt.Sprintf("read 0x%08X request", address), command); err != nil {
		return 0, err
	}
	if err := client.wait(ctx, enhancedCOMReadSettle); err != nil {
		return 0, client.failReadLocked(address, len(command), err)
	}

	response := make([]byte, 0, enhancedCOMMaximumResponseSize)
	buffer := make([]byte, enhancedCOMMaximumResponseSize+1)
	for {
		if err := ctx.Err(); err != nil {
			return 0, client.failReadLocked(address, len(command), err)
		}
		readDeadline := minTime(deadline, time.Now().Add(enhancedCOMReadQuiet))
		if err := client.transport.SetReadDeadline(readDeadline); err != nil {
			return 0, client.failReadLocked(address, len(command), fmt.Errorf("set read deadline: %w", err))
		}

		count, readErr := client.transport.Read(buffer)
		if count < 0 || count > len(buffer) {
			return 0, client.failReadLocked(address, len(command), fmt.Errorf("invalid serial read count %d", count))
		}
		if len(response)+count > enhancedCOMMaximumResponseSize {
			return 0, client.failReadLocked(
				address,
				len(command),
				fmt.Errorf("read response exceeds %d bytes", enhancedCOMMaximumResponseSize),
			)
		}
		response = append(response, buffer[:count]...)

		if count == 0 && readErr == nil {
			readErr = errEnhancedCOMReadTimeout
		}
		if readErr != nil && !isEnhancedCOMTimeout(readErr) {
			return 0, client.failReadLocked(address, len(command), readErr)
		}
		if count != 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return 0, client.failReadLocked(address, len(command), err)
		}
		if len(response) == 0 {
			cause := errEnhancedCOMReadTimeout
			if readErr != nil && !errors.Is(readErr, errEnhancedCOMReadTimeout) {
				cause = errors.Join(cause, readErr)
			}
			return 0, client.failReadLocked(address, len(command), cause)
		}

		value, parseErr := parseEnhancedCOMReadResponse(response)
		if parseErr != nil {
			return 0, client.failReadLocked(address, len(command), parseErr)
		}
		return value, nil
	}
}

func (client *enhancedCOMClient) writeRegister(ctx context.Context, address, value uint32) error {
	client.mu.Lock()
	defer client.mu.Unlock()

	ctx, deadline, finish, err := client.beginLocked(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return client.writeLocked(
		ctx,
		deadline,
		fmt.Sprintf("register write 0x%08X", address),
		encodeEnhancedCOMRegisterWrite(address, value),
	)
}

func (client *enhancedCOMClient) writeBlock(ctx context.Context, address uint32, data []byte) error {
	command, err := encodeEnhancedCOMBlockWrite(address, data)
	if err != nil {
		return err
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	ctx, deadline, finish, err := client.beginLocked(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return client.writeLocked(ctx, deadline, fmt.Sprintf("block write 0x%08X", address), command)
}

func (client *enhancedCOMClient) beginLocked(
	ctx context.Context,
) (context.Context, time.Time, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, time.Time{}, nil, err
	}
	if client.closed.Load() {
		return ctx, time.Time{}, nil, errEnhancedCOMClosed
	}
	if client.unusable {
		return ctx, time.Time{}, nil, errEnhancedCOMUnusable
	}
	deadline := time.Now().Add(client.operationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	operationContext, cancel := context.WithDeadline(ctx, deadline)
	stopLifecycle := context.AfterFunc(client.lifecycle, cancel)
	if client.lifecycle.Err() != nil {
		cancel()
	}
	finish := func() {
		stopLifecycle()
		cancel()
	}
	if err := operationContext.Err(); err != nil {
		finish()
		if client.closed.Load() {
			return operationContext, time.Time{}, nil, errEnhancedCOMClosed
		}
		return operationContext, time.Time{}, nil, err
	}
	return operationContext, deadline, finish, nil
}

func (client *enhancedCOMClient) writeLocked(
	ctx context.Context,
	deadline time.Time,
	operation string,
	command []byte,
) error {
	if err := client.transport.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set Enhanced COM %s deadline: %w", operation, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	written, writeErr := client.transport.Write(command)
	postWriteErr := ctx.Err()
	// Closing the client also cancels ctx. Preserve the stronger lifecycle
	// identity instead of exposing a scheduler-dependent context.Canceled.
	if client.closed.Load() {
		postWriteErr = errEnhancedCOMClosed
	}
	if written == len(command) && writeErr == nil && postWriteErr == nil {
		return nil
	}
	if written < 0 || written > len(command) {
		writeErr = fmt.Errorf("invalid serial write count %d", written)
	} else if writeErr == nil {
		if postWriteErr != nil {
			writeErr = postWriteErr
		} else {
			writeErr = io.ErrShortWrite
		}
	}
	if postWriteErr != nil {
		writeErr = postWriteErr
	}
	client.unusable = true
	return &enhancedCOMUnknownResultError{
		operation: operation,
		written:   written,
		expected:  len(command),
		cause:     writeErr,
	}
}

func (client *enhancedCOMClient) failReadLocked(address uint32, requestSize int, cause error) error {
	client.unusable = true
	return &enhancedCOMUnknownResultError{
		operation: fmt.Sprintf("read 0x%08X", address),
		written:   requestSize,
		expected:  requestSize,
		cause:     cause,
	}
}

func (client *enhancedCOMClient) failSubmittedLocked(operation string, commandSize int, cause error) error {
	client.unusable = true
	return &enhancedCOMUnknownResultError{
		operation: operation,
		written:   commandSize,
		expected:  commandSize,
		cause:     cause,
	}
}

func minTime(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

func isEnhancedCOMTimeout(err error) bool {
	if errors.Is(err, errEnhancedCOMReadTimeout) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	timeout, ok := errors.AsType[net.Error](err)
	return ok && timeout.Timeout()
}

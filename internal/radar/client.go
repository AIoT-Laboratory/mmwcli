package radar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaximumResponseBytes = 1024 * 1024
	commandReadPollInterval     = 100 * time.Millisecond
	commandWriteBound           = time.Second
)

// ErrReadTimeout is returned when the configured serial transport reports its
// timeout as (0, nil), as both Windows COMMTIMEOUTS and Linux VMIN=0/VTIME may
// do. A command that reaches this error has an unknown device-side result.
var ErrReadTimeout = errors.New("radar transport read timeout")

// Transport is the deliberately small boundary between the radar protocol and
// a platform-specific, deadline-capable serial implementation.
type Transport interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type inputPurger interface {
	PurgeInput() error
}

// ResponseError means a command did not reach an explicit Done/Error terminal
// response. The device state is unknown and callers must not blindly retry the
// command.
type ResponseError struct {
	Command  string
	Response string
	Cause    error
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("radar command %q ended without an explicit Done/Error terminal; result is unknown: %v", e.Command, e.Cause)
}

func (e *ResponseError) Unwrap() error { return e.Cause }

// Client serializes text CLI exchanges over one already-open transport.
type Client struct {
	transport        Transport
	dialect          Dialect
	maximumResponse  int
	commandTimeout   time.Duration
	platformVerified bool
	desynchronized   bool
	closed           atomic.Bool
	mu               sync.Mutex
	closeOnce        sync.Once
	closeErr         error
}

func NewClient(transport Transport, dialect Dialect, commandTimeout time.Duration) (*Client, error) {
	if transport == nil {
		return nil, errors.New("radar transport is nil")
	}
	if !dialect.valid() {
		return nil, errors.New("invalid radar CLI dialect")
	}
	if commandTimeout <= 0 {
		return nil, errors.New("radar command timeout must be positive")
	}
	return &Client{
		transport:       transport,
		dialect:         dialect,
		maximumResponse: defaultMaximumResponseBytes,
		commandTimeout:  commandTimeout,
	}, nil
}

func (c *Client) Dialect() Dialect { return c.dialect }

// Close closes the injected transport. It is safe to call more than once.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		// Close without waiting for the command mutex. Normal cancellation uses
		// the short read deadlines above; Close also asks the OS to release any
		// outstanding synchronous operation without assuming immediate wake-up.
		c.closeErr = c.transport.Close()
	})
	return c.closeErr
}

// SendCommand is the low-level exchange primitive. It does not implicitly run
// the StudioCLI platform gate; use VerifyPlatform, Apply, Start, or Stop for
// state-changing workflows.
func (c *Client) SendCommand(command string) (string, error) {
	return c.SendCommandContext(context.Background(), command)
}

// SendCommandContext is SendCommand with a cancellation/deadline bound. It
// performs no detached work: cancellation is observed by repeatedly applying
// short transport deadlines while the command mutex remains owned.
func (c *Client) SendCommandContext(ctx context.Context, command string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendCommandLocked(ctx, command)
}

func (c *Client) sendCommandLocked(ctx context.Context, command string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.closed.Load() {
		return "", errors.New("radar client is closed")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("radar command is empty")
	}
	if strings.ContainsAny(command, "\r\n") {
		return "", errors.New("radar command must be exactly one line")
	}
	if purger, ok := c.transport.(inputPurger); ok {
		if err := purger.PurgeInput(); err != nil {
			return "", fmt.Errorf("purge stale radar input before %q: %w", command, err)
		}
	}

	deadline := time.Now().Add(c.commandTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	writeDeadline := time.Now().Add(commandWriteBound)
	if deadline.Before(writeDeadline) {
		writeDeadline = deadline
	}
	if err := c.transport.SetWriteDeadline(writeDeadline); err != nil {
		return "", fmt.Errorf("set radar command %q write deadline: %w", command, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	payload := command + "\n"
	written, err := io.WriteString(c.transport, payload)
	if err != nil {
		if written > 0 {
			c.desynchronized = true
			return "", c.unknownResult(command, "", contextOrTimeoutCause(ctx, deadline, err))
		}
		return "", fmt.Errorf("write radar command %q: %w", command, contextOrTimeoutCause(ctx, deadline, err))
	}
	if written != len(payload) {
		if written > 0 {
			c.desynchronized = true
			return "", c.unknownResult(command, "", io.ErrShortWrite)
		}
		return "", fmt.Errorf("write radar command %q: %w", command, io.ErrShortWrite)
	}
	requireEcho := c.desynchronized
	c.desynchronized = true

	var response strings.Builder
	var pendingLine strings.Builder
	echoSeen := !requireEcho
	buffer := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return response.String(), c.unknownResult(command, response.String(), err)
		}
		if !time.Now().Before(deadline) {
			return response.String(), c.unknownResult(command, response.String(), contextOrTimeoutCause(ctx, deadline, ErrReadTimeout))
		}
		readDeadline := time.Now().Add(commandReadPollInterval)
		if deadline.Before(readDeadline) {
			readDeadline = deadline
		}
		if err := c.transport.SetReadDeadline(readDeadline); err != nil {
			return response.String(), c.unknownResult(command, response.String(), fmt.Errorf("set read deadline: %w", err))
		}
		count, readErr := c.transport.Read(buffer)
		if err := ctx.Err(); err != nil {
			return response.String(), c.unknownResult(command, response.String(), err)
		}
		if !time.Now().Before(deadline) {
			return response.String(), c.unknownResult(command, response.String(), contextOrTimeoutCause(ctx, deadline, ErrReadTimeout))
		}
		for _, value := range buffer[:count] {
			if response.Len() == c.maximumResponse {
				return response.String(), c.unknownResult(
					command,
					response.String(),
					fmt.Errorf("response exceeded %d bytes", c.maximumResponse),
				)
			}
			_ = response.WriteByte(value)
			_ = pendingLine.WriteByte(value)
			if value != '\n' {
				continue
			}
			completeLine := pendingLine.String()
			pendingLine.Reset()
			if !echoSeen && responseLineEchoes(command, completeLine) {
				echoSeen = true
			}
			if !echoSeen {
				continue
			}
			terminal := FindTerminal(completeLine)
			switch terminal.Status {
			case TerminalDone:
				c.desynchronized = false
				return response.String(), nil
			case TerminalError:
				c.desynchronized = false
				return response.String(), newCommandError(c.dialect, command, response.String(), terminal.ErrorCode)
			}
		}
		if count == 0 && readErr == nil {
			readErr = ErrReadTimeout
		}
		if readErr != nil {
			if isTimeoutError(readErr) {
				continue
			}
			return response.String(), c.unknownResult(command, response.String(), readErr)
		}
	}
}

func (c *Client) unknownResult(command, response string, cause error) error {
	c.desynchronized = true
	return &ResponseError{Command: command, Response: response, Cause: cause}
}

func responseLineEchoes(command, rawLine string) bool {
	line := strings.TrimSpace(strings.ReplaceAll(rawLine, "\r", ""))
	return line == command || strings.HasSuffix(line, ">"+command)
}

func isTimeoutError(err error) bool {
	if errors.Is(err, ErrReadTimeout) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	timeoutError, ok := errors.AsType[net.Error](err)
	return ok && timeoutError.Timeout()
}

func contextOrTimeoutCause(ctx context.Context, deadline time.Time, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if contextDeadline, ok := ctx.Deadline(); ok && !contextDeadline.After(deadline) && !time.Now().Before(contextDeadline) {
		return context.DeadlineExceeded
	}
	return fallback
}

// VerifyPlatform sends version once for StudioCLI and requires an exact
// Platform: xWR68xx field. SDKDemo has no version gate and performs no I/O.
func (c *Client) VerifyPlatform() (string, error) {
	return c.VerifyPlatformContext(context.Background())
}

func (c *Client) VerifyPlatformContext(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.verifyPlatformLocked(ctx)
}

func (c *Client) verifyPlatformLocked(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.platformVerified {
		return "", nil
	}
	if !c.dialect.RequiresPlatformVerification() {
		c.platformVerified = true
		return "", nil
	}
	response, err := c.sendCommandLocked(ctx, "version")
	if err != nil {
		return response, err
	}
	if err := c.dialect.VerifyPlatformResponse(response); err != nil {
		return response, err
	}
	c.platformVerified = true
	return response, nil
}

// Apply sends only a full plan's configuration commands. BuildCapturePlan has
// already removed sensorStart so a capture coordinator can arm its data sink
// before calling Start.
func (c *Client) Apply(plan CapturePlan) error {
	return c.ApplyContext(context.Background(), plan)
}

func (c *Client) ApplyContext(ctx context.Context, plan CapturePlan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.Dialect != c.dialect {
		return fmt.Errorf("capture plan dialect %q does not match client dialect %q", plan.Dialect.Name(), c.dialect.Name())
	}
	if plan.Mode != FullConfiguration {
		return errors.New("reuse plan must not resend radar configuration")
	}
	// CapturePlan fields are exported for orchestration and reporting, so do not
	// trust a manually constructed value at the hardware boundary. Rebuild the
	// full plan to repeat every raw-only and capture-contract check before I/O.
	preflightCommands := append([]string(nil), plan.ConfigurationCommands...)
	preflightCommands = append(preflightCommands, "sensorStart")
	if _, err := BuildCapturePlan(c.dialect, preflightCommands, FullConfiguration); err != nil {
		return err
	}
	if _, err := c.verifyPlatformLocked(ctx); err != nil {
		return err
	}
	for _, command := range plan.ConfigurationCommands {
		if _, err := c.sendCommandLocked(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

// Start begins a newly configured frame using the exact sensorStart command.
func (c *Client) Start() (string, error) {
	return c.StartContext(context.Background())
}

func (c *Client) StartContext(ctx context.Context) (string, error) {
	return c.start(ctx, "sensorStart")
}

// StartWithoutReconfiguration reuses the device's existing configuration.
func (c *Client) StartWithoutReconfiguration() (string, error) {
	return c.StartWithoutReconfigurationContext(context.Background())
}

func (c *Client) StartWithoutReconfigurationContext(ctx context.Context) (string, error) {
	return c.start(ctx, "sensorStart 0")
}

func (c *Client) start(ctx context.Context, command string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.verifyPlatformLocked(ctx); err != nil {
		return "", err
	}
	return c.sendCommandLocked(ctx, command)
}

// Stop sends sensorStop after the platform gate. StudioCLI Error -54 is
// returned as idempotent success only for this command.
func (c *Client) Stop() (string, error) {
	return c.StopContext(context.Background())
}

func (c *Client) StopContext(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.verifyPlatformLocked(ctx); err != nil {
		return "", err
	}
	response, err := c.sendCommandLocked(ctx, "sensorStop")
	if IsAlreadyStopped(err) {
		return response, nil
	}
	return response, err
}

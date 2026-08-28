package dca

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	controlReadPollInterval = 100 * time.Millisecond
	asyncStatusQueueLimit   = 32
)

var (
	ErrCommandTimeout           = errors.New("DCA1000 command response timeout")
	ErrClientPoisoned           = errors.New("DCA1000 control client state is unknown")
	ErrAsyncStatusQueueOverflow = errors.New("DCA1000 async status queue overflow")
)

// Options configures the DCA1000 UDP control client. DefaultOptions binds the
// control socket to 0.0.0.0:4096, matching TI's reference implementation.
// Tests and explicitly isolated deployments may override ControlBindPort,
// including setting it to zero for an ephemeral port.
type Options struct {
	DeviceAddress      net.IP
	ControlPort        int
	ControlBindAddress net.IP
	ControlBindPort    int
	Timeout            time.Duration
}

func DefaultOptions() Options {
	return Options{
		DeviceAddress:      net.ParseIP("192.168.33.180"),
		ControlPort:        4096,
		ControlBindAddress: net.IPv4zero,
		ControlBindPort:    4096,
		Timeout:            3 * time.Second,
	}
}

// CommandError identifies the operation and command whose network I/O failed.
type CommandError struct {
	Operation string
	Command   Command
	Err       error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("DCA1000 %s %s: %v", e.Operation, e.Command, e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

// StatusError reports a syntactically valid response whose FPGA status is not
// zero.
type StatusError struct {
	Command Command
	Status  uint16
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("DCA1000 %s returned status %d (0x%04X)", e.Command, e.Status, e.Status)
}

// StartConvergenceError reports a failed or unconfirmed StartRecord and the
// outcome of the one permitted best-effort StopRecord convergence command.
type StartConvergenceError struct {
	StartErr error
	StopErr  error
}

func (e *StartConvergenceError) Error() string {
	if e.StopErr != nil {
		return fmt.Sprintf("DCA1000 start was not confirmed and the one-shot stop convergence failed: start: %v; stop: %v", e.StartErr, e.StopErr)
	}
	return fmt.Sprintf("DCA1000 start was not confirmed; start was not retried and one stop command converged state: %v", e.StartErr)
}

func (e *StartConvergenceError) Unwrap() []error {
	if e.StopErr == nil {
		return []error{e.StartErr}
	}
	return []error{e.StartErr, e.StopErr}
}

// CleanupFailed reports whether the one permitted StopRecord convergence
// attempt also failed. It lets callers distinguish a clean cancellation from
// a cancellation that left DCA recording state unknown.
func (e *StartConvergenceError) CleanupFailed() bool { return e != nil && e.StopErr != nil }

// ConfigurationResponses contains the two replies produced by Configure.
type ConfigurationResponses struct {
	FPGA   Response
	Record Response
}

// Client serializes commands over one UDP socket. A DCA control client must not
// be used for concurrent commands because every instance owns one local control
// port and responses have no transaction identifier beyond the command code.
type Client struct {
	conn        *net.UDPConn
	destination *net.UDPAddr
	timeout     time.Duration

	executeMu   sync.Mutex
	stateMu     sync.Mutex
	closed      bool
	last        *net.UDPAddr
	async       []Response
	poisonCause error
}

// Dial binds the UDP control socket immediately. Callers should normally pass
// DefaultOptions with only intentional overrides applied.
func Dial(options Options) (*Client, error) {
	deviceIP, err := requireIPv4("device address", options.DeviceAddress)
	if err != nil {
		return nil, err
	}
	bindIP, err := requireIPv4("control bind address", options.ControlBindAddress)
	if err != nil {
		return nil, err
	}
	if options.ControlPort < 1 || options.ControlPort > 65535 {
		return nil, fmt.Errorf("DCA1000 control port must be in 1..65535, got %d", options.ControlPort)
	}
	if options.ControlBindPort < 0 || options.ControlBindPort > 65535 {
		return nil, fmt.Errorf("DCA1000 control bind port must be in 0..65535, got %d", options.ControlBindPort)
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("DCA1000 command timeout must be positive, got %s", options.Timeout)
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: bindIP, Port: options.ControlBindPort})
	if err != nil {
		return nil, fmt.Errorf("bind DCA1000 control socket %s:%d: %w", bindIP, options.ControlBindPort, err)
	}
	return &Client{
		conn:        conn,
		destination: &net.UDPAddr{IP: deviceIP, Port: options.ControlPort},
		timeout:     options.Timeout,
	}, nil
}

// TakeStatuses returns and clears valid asynchronous status frames seen
// while waiting for commands. They are never mistaken for synchronous replies.
func (client *Client) TakeStatuses() []Response {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	statuses := make([]Response, len(client.async))
	copy(statuses, client.async)
	client.async = client.async[:0]
	return statuses
}

// DrainStatuses keeps reading the control socket until no valid DCA1000
// frame has arrived for quietWindow. ctx should carry the absolute upper bound
// required by the capture lifecycle. The returned slice includes statuses
// already observed by Execute and removes them from the client queue.
func (client *Client) DrainStatuses(ctx context.Context, quietWindow time.Duration) ([]Response, error) {
	if client == nil {
		return nil, errors.New("nil DCA1000 client")
	}
	if quietWindow <= 0 {
		return nil, fmt.Errorf("DCA1000 control quiet window must be positive, got %s", quietWindow)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client.executeMu.Lock()
	defer client.executeMu.Unlock()
	if err := client.usabilityError(); err != nil {
		return client.TakeStatuses(), err
	}

	quietDeadline := time.Now().Add(quietWindow)
	buffer := make([]byte, 65535)
	for {
		if err := ctx.Err(); err != nil {
			return client.TakeStatuses(), err
		}
		now := time.Now()
		if !now.Before(quietDeadline) {
			return client.TakeStatuses(), nil
		}
		readDeadline := now.Add(controlReadPollInterval)
		if quietDeadline.Before(readDeadline) {
			readDeadline = quietDeadline
		}
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(readDeadline) {
			readDeadline = contextDeadline
		}
		if err := client.conn.SetReadDeadline(readDeadline); err != nil {
			return client.TakeStatuses(), fmt.Errorf("set DCA1000 control drain deadline: %w", err)
		}
		length, source, err := client.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return client.TakeStatuses(), ctxErr
			}
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				continue
			}
			return client.TakeStatuses(), fmt.Errorf("drain DCA1000 control socket: %w", err)
		}
		response, err := ParseResponse(buffer[:length])
		if err != nil {
			continue
		}
		quietDeadline = time.Now().Add(quietWindow)
		if response.Command != CommandAsyncStatus {
			continue
		}
		response.Source = cloneUDPAddr(source)
		if err := client.enqueueAsyncStatus(response); err != nil {
			return client.TakeStatuses(), err
		}
	}
}

// Execute sends a command exactly once and waits for a matching fixed-size
// response. Like TI's reference recvfrom loop, it deliberately accepts the
// response from any IPv4 source endpoint. Malformed, extended, unrelated, and
// asynchronous datagrams do not satisfy the pending command.
func (client *Client) Execute(ctx context.Context, command Command, payload []byte) (Response, error) {
	if client == nil {
		return Response{}, errors.New("nil DCA1000 client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := BuildRequest(command, payload)
	if err != nil {
		return Response{}, err
	}

	client.executeMu.Lock()
	defer client.executeMu.Unlock()
	if err := client.usabilityError(); err != nil {
		return Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	commandDeadline := time.Now().Add(client.timeout)
	effectiveDeadline, contextDeadline := earlierDeadline(ctx, commandDeadline)
	if err := client.conn.SetWriteDeadline(effectiveDeadline); err != nil {
		return Response{}, &CommandError{Operation: "set write deadline for", Command: command, Err: err}
	}
	if _, err := client.conn.WriteToUDP(request, client.destination); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, ctxErr
		}
		return Response{}, &CommandError{Operation: "send", Command: command, Err: err}
	}

	buffer := make([]byte, 65535)
	for {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		now := time.Now()
		if !now.Before(effectiveDeadline) {
			return Response{}, commandDeadlineError(command, contextDeadline)
		}
		pollDeadline := now.Add(controlReadPollInterval)
		if effectiveDeadline.Before(pollDeadline) {
			pollDeadline = effectiveDeadline
		}
		if err := client.conn.SetReadDeadline(pollDeadline); err != nil {
			return Response{}, &CommandError{Operation: "set read deadline for", Command: command, Err: err}
		}
		length, source, err := client.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Response{}, ctxErr
			}
			if errors.Is(err, net.ErrClosed) {
				return Response{}, &CommandError{Operation: "receive", Command: command, Err: err}
			}
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				if !time.Now().Before(effectiveDeadline) {
					return Response{}, commandDeadlineError(command, contextDeadline)
				}
				continue
			}
			return Response{}, &CommandError{Operation: "receive", Command: command, Err: err}
		}

		response, err := ParseResponse(buffer[:length])
		if err != nil {
			continue
		}
		response.Source = cloneUDPAddr(source)
		if response.Command == CommandAsyncStatus {
			if err := client.enqueueAsyncStatus(response); err != nil {
				return Response{}, err
			}
			continue
		}
		if response.Command != command {
			continue
		}

		client.stateMu.Lock()
		client.last = cloneUDPAddr(source)
		client.stateMu.Unlock()
		return response, nil
	}
}

func (client *Client) Ping(ctx context.Context) (Response, error) {
	return client.Execute(ctx, CommandSystemAlive, nil)
}

func (client *Client) ReadFPGAVersion(ctx context.Context) (Response, error) {
	return client.Execute(ctx, CommandReadFPGAVersion, nil)
}

func (client *Client) Version(ctx context.Context) (FPGAVersion, error) {
	response, err := client.ReadFPGAVersion(ctx)
	if err != nil {
		return FPGAVersion{}, err
	}
	return DecodeFPGAVersion(response.Status), nil
}

func (client *Client) ConfigureFPGA(ctx context.Context, config FPGAConfig) (Response, error) {
	payload, err := BuildFPGAConfig(config)
	if err != nil {
		return Response{}, err
	}
	return client.Execute(ctx, CommandConfigureFPGA, payload)
}

func (client *Client) ConfigureRecord(ctx context.Context, delayMicroseconds int) (Response, error) {
	payload, err := BuildRecordConfig(delayMicroseconds)
	if err != nil {
		return Response{}, err
	}
	return client.Execute(ctx, CommandConfigureRecord, payload)
}

// Configure applies FPGA mode followed by packet settings. It does not reset
// the FPGA and aborts immediately if either response has non-zero status.
func (client *Client) Configure(ctx context.Context, config FPGAConfig, delayMicroseconds int) (ConfigurationResponses, error) {
	var responses ConfigurationResponses
	response, err := client.ConfigureFPGA(ctx, config)
	responses.FPGA = response
	if err != nil {
		return responses, err
	}
	if response.Status != 0 {
		return responses, &StatusError{Command: response.Command, Status: response.Status}
	}
	response, err = client.ConfigureRecord(ctx, delayMicroseconds)
	responses.Record = response
	if err != nil {
		return responses, err
	}
	if response.Status != 0 {
		return responses, &StatusError{Command: response.Command, Status: response.Status}
	}
	return responses, nil
}

func (client *Client) StartRecord(ctx context.Context) (Response, error) {
	return client.Execute(ctx, CommandStartRecord, nil)
}

func (client *Client) Stop(ctx context.Context) (Response, error) {
	return client.Execute(ctx, CommandStopRecord, nil)
}

// Start sends StartRecord only once. If its response is
// missing, invalid, or non-zero, it uses a fresh bounded context to send exactly
// one StopRecord and returns an error; StartRecord is never retried.
func (client *Client) Start(ctx context.Context) (Response, error) {
	if client == nil {
		return Response{}, errors.New("nil DCA1000 client")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
	}
	response, startErr := client.StartRecord(ctx)
	if startErr == nil && response.Status == 0 {
		return response, nil
	}
	if startErr == nil {
		startErr = &StatusError{Command: response.Command, Status: response.Status}
	}

	stopContext, cancel := context.WithTimeout(context.Background(), client.timeout)
	defer cancel()
	stopResponse, stopErr := client.Stop(stopContext)
	if stopErr == nil && stopResponse.Status != 0 {
		stopErr = &StatusError{Command: stopResponse.Command, Status: stopResponse.Status}
	}
	return response, &StartConvergenceError{StartErr: startErr, StopErr: stopErr}
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.stateMu.Lock()
	if client.closed {
		client.stateMu.Unlock()
		return nil
	}
	client.closed = true
	client.stateMu.Unlock()
	return client.conn.Close()
}

func (client *Client) usabilityError() error {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if client.closed {
		return net.ErrClosed
	}
	if client.poisonCause != nil {
		return fmt.Errorf("%w: previous failure: %w", ErrClientPoisoned, client.poisonCause)
	}
	return nil
}

func (client *Client) enqueueAsyncStatus(response Response) error {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if len(client.async) >= asyncStatusQueueLimit {
		cause := fmt.Errorf(
			"%w: reached the %d-status limit before status 0x%04X",
			ErrAsyncStatusQueueOverflow,
			asyncStatusQueueLimit,
			response.Status,
		)
		if client.poisonCause == nil {
			client.poisonCause = cause
		}
		return fmt.Errorf("%w: %w", ErrClientPoisoned, client.poisonCause)
	}
	client.async = append(client.async, response)
	return nil
}

func earlierDeadline(ctx context.Context, commandDeadline time.Time) (time.Time, bool) {
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(commandDeadline) {
		return deadline, true
	}
	return commandDeadline, false
}

func commandDeadlineError(command Command, contextDeadline bool) error {
	if contextDeadline {
		return context.DeadlineExceeded
	}
	return &CommandError{Operation: "wait for", Command: command, Err: ErrCommandTimeout}
}

func requireIPv4(name string, address net.IP) (net.IP, error) {
	if address == nil {
		return nil, fmt.Errorf("DCA1000 %s is required", name)
	}
	address = address.To4()
	if address == nil {
		return nil, fmt.Errorf("DCA1000 %s must be IPv4", name)
	}
	return append(net.IP(nil), address...), nil
}

func cloneUDPAddr(endpoint *net.UDPAddr) *net.UDPAddr {
	if endpoint == nil {
		return nil
	}
	return &net.UDPAddr{
		IP:   append(net.IP(nil), endpoint.IP...),
		Port: endpoint.Port,
		Zone: endpoint.Zone,
	}
}

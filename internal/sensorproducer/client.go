package sensorproducer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	DefaultQueueSize = 16
	MaxQueueSize     = 1024
)

var (
	ErrQueueFull    = errors.New("sensor producer record queue is full")
	ErrClientClosed = errors.New("sensor producer client is closed")
)

type ClientOptions struct {
	QueueSize int
}

type ackResult struct {
	err error
}

type Client struct {
	input   io.WriteCloser
	output  io.ReadCloser
	control *ControlEncoder
	decoder *Decoder
	state   *State

	controlMu  sync.Mutex
	closeOnce  sync.Once
	finishOnce sync.Once
	mu         sync.Mutex
	abortErr   error
	terminal   error

	acks    chan ackResult
	records chan Record
	done    chan struct{}
}

func NewClient(
	input io.WriteCloser,
	output io.ReadCloser,
	sessionID string,
	sourceID string,
	options ClientOptions,
) (*Client, error) {
	if input == nil || output == nil {
		return nil, errors.New("sensor producer input and output pipes are required")
	}
	queueSize := options.QueueSize
	if queueSize == 0 {
		queueSize = DefaultQueueSize
	}
	if queueSize < 1 || queueSize > MaxQueueSize {
		return nil, fmt.Errorf("sensor producer queue size must be in 1..%d", MaxQueueSize)
	}
	control, err := NewControlEncoder(input)
	if err != nil {
		return nil, err
	}
	decoder, err := NewDecoder(output)
	if err != nil {
		return nil, err
	}
	state, err := NewState(sessionID, sourceID)
	if err != nil {
		return nil, err
	}
	client := &Client{
		input: input, output: output, control: control, decoder: decoder, state: state,
		acks: make(chan ackResult, 1), records: make(chan Record, queueSize), done: make(chan struct{}),
	}
	go client.run()
	return client, nil
}

func (client *Client) Ready(ctx context.Context) error  { return client.command(ctx, CommandReady) }
func (client *Client) Arm(ctx context.Context) error    { return client.command(ctx, CommandArm) }
func (client *Client) Start(ctx context.Context) error  { return client.command(ctx, CommandStart) }
func (client *Client) Stop(ctx context.Context) error   { return client.command(ctx, CommandStop) }
func (client *Client) Cancel(ctx context.Context) error { return client.command(ctx, CommandCancel) }

func (client *Client) Phase() Phase {
	if client == nil || client.state == nil {
		return PhaseFailed
	}
	return client.state.Phase()
}

func (client *Client) Next(ctx context.Context) (Record, error) {
	var zero Record
	if client == nil {
		return zero, errors.New("sensor producer client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case record, ok := <-client.records:
		if ok {
			return record, nil
		}
		return zero, client.result()
	default:
	}
	select {
	case record, ok := <-client.records:
		if ok {
			return record, nil
		}
		return zero, client.result()
	case <-client.done:
		select {
		case record, ok := <-client.records:
			if ok {
				return record, nil
			}
		default:
		}
		return zero, client.result()
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (client *Client) Wait(ctx context.Context) error {
	if client == nil {
		return errors.New("sensor producer client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-client.done:
		return client.terminalResult()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.abort(ErrClientClosed)
	<-client.done
	return client.terminalResult()
}

func (client *Client) command(ctx context.Context, command Command) error {
	if client == nil {
		return errors.New("sensor producer client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client.controlMu.Lock()
	defer client.controlMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-client.done:
		return client.result()
	default:
	}
	control, err := client.state.Prepare(command)
	if err != nil {
		return err
	}
	canceled := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() {
		client.abort(ctx.Err())
		close(canceled)
	})
	if err := client.control.Write(control); err != nil {
		client.abort(err)
		if !stopCancellation() {
			<-canceled
		}
		return err
	}
	select {
	case result := <-client.acks:
		if !stopCancellation() {
			<-canceled
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return result.err
	case <-client.done:
		if !stopCancellation() {
			<-canceled
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return client.result()
	case <-ctx.Done():
		if !stopCancellation() {
			<-canceled
		}
		return ctx.Err()
	}
}

func (client *Client) run() {
	for {
		record, err := client.decoder.Read()
		if errors.Is(err, io.EOF) {
			client.finish(client.state.AcceptTransportEOF())
			return
		}
		if err != nil {
			if cause := client.abortCause(); cause != nil {
				client.finish(cause)
			} else {
				client.finish(err)
			}
			return
		}
		if record.Type == FrameACK {
			ackErr := client.state.Accept(record)
			select {
			case client.acks <- ackResult{err: ackErr}:
			default:
				client.finish(fmt.Errorf("%w: ACK queue is full", ErrProtocol))
				return
			}
			var producerFailure *ProducerError
			if ackErr != nil && !errors.As(ackErr, &producerFailure) {
				client.finish(ackErr)
				return
			}
			continue
		}
		if err := client.state.Accept(record); err != nil {
			client.finish(err)
			return
		}
		select {
		case client.records <- record:
		default:
			client.finish(ErrQueueFull)
			return
		}
	}
}

func (client *Client) abort(err error) {
	if err == nil {
		err = ErrClientClosed
	}
	client.mu.Lock()
	if client.abortErr == nil {
		client.abortErr = err
	}
	client.mu.Unlock()
	client.closeTransport()
}

func (client *Client) finish(err error) {
	client.finishOnce.Do(func() {
		client.mu.Lock()
		if err == nil {
			err = client.abortErr
		}
		client.terminal = err
		client.mu.Unlock()
		client.closeTransport()
		close(client.records)
		close(client.done)
	})
}

func (client *Client) closeTransport() {
	client.closeOnce.Do(func() {
		_ = client.input.Close()
		_ = client.output.Close()
	})
}

func (client *Client) abortCause() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.abortErr
}

func (client *Client) terminalResult() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.terminal
}

func (client *Client) result() error {
	if err := client.terminalResult(); err != nil {
		return err
	}
	return io.EOF
}

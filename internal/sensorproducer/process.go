package sensorproducer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type ProcessOptions struct {
	Stderr    io.Writer
	QueueSize int
	Dir       string
	Env       []string
}

type Process struct {
	command *exec.Cmd
	client  *Client
	done    chan struct{}

	killOnce sync.Once
	mu       sync.Mutex
	waitErr  error
}

// ConvergenceError means the caller's bound elapsed before Process could prove
// that its child had exited and been reaped. It deliberately does not classify
// a producer control or exit error: those remain ordinary source failures once
// the process has converged.
type ConvergenceError struct {
	operation string
	err       error
}

func (err *ConvergenceError) Error() string {
	return fmt.Sprintf("sensor producer process did not converge during %s: %v", err.operation, err.err)
}

func (err *ConvergenceError) Unwrap() error { return err.err }

func newConvergenceError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &ConvergenceError{operation: operation, err: err}
}

// IsConvergenceError reports whether err includes a failure to prove that the
// child process was reaped within a caller-provided bound.
func IsConvergenceError(err error) bool {
	var convergence *ConvergenceError
	return errors.As(err, &convergence)
}

func StartProcess(
	ctx context.Context,
	argv []string,
	sessionID string,
	sourceID string,
	options ProcessOptions,
) (*Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(argv) == 0 || argv[0] == "" || strings.IndexByte(argv[0], 0) >= 0 {
		return nil, errors.New("sensor producer command argv[0] is required and cannot contain NUL")
	}
	for index, argument := range argv[1:] {
		if strings.IndexByte(argument, 0) >= 0 {
			return nil, fmt.Errorf("sensor producer command argument %d contains NUL", index+1)
		}
	}
	if _, err := NewState(sessionID, sourceID); err != nil {
		return nil, err
	}
	if options.QueueSize < 0 || options.QueueSize > MaxQueueSize {
		return nil, fmt.Errorf("sensor producer queue size must be in 1..%d or zero for the default", MaxQueueSize)
	}

	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = options.Dir
	if options.Env != nil {
		command.Env = append([]string(nil), options.Env...)
	}
	if options.Stderr == nil {
		command.Stderr = os.Stderr
	} else {
		command.Stderr = options.Stderr
	}
	input, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create sensor producer stdin: %w", err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("create sensor producer stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("start sensor producer: %w", err)
	}
	client, err := NewClient(input, output, sessionID, sourceID, ClientOptions{QueueSize: options.QueueSize})
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	process := &Process{command: command, client: client, done: make(chan struct{})}
	go process.reap()
	return process, nil
}

func (process *Process) Client() *Client {
	if process == nil {
		return nil
	}
	return process.client
}

func (process *Process) Cancel(ctx context.Context) error {
	if process == nil || process.client == nil {
		return errors.New("sensor producer process is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	controlErr := process.client.Cancel(ctx)
	waitErr := process.Wait(ctx)
	if !IsConvergenceError(waitErr) {
		// A terminal client may reject CANCEL because it exited between the
		// caller's intent and command dispatch. Its reaped result is authoritative.
		return waitErr
	}

	// No terminal result was available within the bound. Closing the transport
	// unblocks the protocol reader, then one best-effort kill asks the OS to reap
	// the child. Do not wait again after the context has expired.
	process.client.abort(controlErr)
	killErr := process.kill()
	return errors.Join(controlErr, killErr, waitErr)
}

func (process *Process) Wait(ctx context.Context) error {
	if process == nil || process.command == nil {
		return errors.New("sensor producer process is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-process.done:
		return errors.Join(process.processResult(), process.client.terminalResult())
	default:
	}
	select {
	case <-process.done:
		return errors.Join(process.processResult(), process.client.terminalResult())
	case <-ctx.Done():
		// If process completion raced the caller's deadline, completion is
		// authoritative. Do not randomly report an already reaped child as
		// unproven merely because both channels were ready in this select.
		select {
		case <-process.done:
			return errors.Join(process.processResult(), process.client.terminalResult())
		default:
		}
		return newConvergenceError("wait", ctx.Err())
	}
}

func (process *Process) Kill() error {
	if process == nil || process.command == nil {
		return nil
	}
	process.client.abort(ErrClientClosed)
	return process.kill()
}

func (process *Process) reap() {
	// StdoutPipe requires the reader to finish before Wait closes the pipe.
	// Every client terminal path closes its owned pipes and then closes done.
	<-process.client.done
	err := process.command.Wait()
	process.mu.Lock()
	process.waitErr = err
	process.mu.Unlock()
	close(process.done)
}

func (process *Process) kill() error {
	var killErr error
	process.killOnce.Do(func() {
		if process.command.Process != nil {
			killErr = process.command.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
		}
	})
	return killErr
}

func (process *Process) processResult() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.waitErr
}

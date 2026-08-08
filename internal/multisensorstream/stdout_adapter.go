package multisensorstream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

var ErrStdoutClosed = errors.New("multisensor stream stdout is closed")

// StdoutAdapter owns one real OS file for one aggregate stream. Encoder calls
// are serialized; Close remains independent so cancellation can interrupt a
// blocked pipe write.
type StdoutAdapter struct {
	encoderMu sync.Mutex
	encoder   *Encoder
	output    *ownedStdoutFile
}

// NewStdoutAdapter transfers exclusive ownership of stdout, including on
// failure, and emits SESSION before returning.
func NewStdoutAdapter(
	ctx context.Context,
	stdout *os.File,
	session Session,
) (*StdoutAdapter, error) {
	if stdout == nil {
		return nil, errors.New("multisensor stream stdout is nil")
	}
	output := &ownedStdoutFile{file: stdout}
	if ctx == nil {
		return nil, errors.Join(errors.New("multisensor stream context is nil"), output.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, output.Close())
	}
	cancellation := armStdoutCancellation(ctx, output)
	encoder, encodeErr := NewEncoder(output, session)
	canceled := cancellation.stopAndWait()
	if canceled || ctx.Err() != nil {
		encodeErr = errors.Join(encodeErr, ctx.Err())
	}
	if encodeErr != nil {
		return nil, errors.Join(encodeErr, output.Close())
	}
	return &StdoutAdapter{encoder: encoder, output: output}, nil
}

func (adapter *StdoutAdapter) WriteRadarConfig(
	ctx context.Context,
	sourceID string,
	format string,
	payload []byte,
) error {
	return adapter.operation(ctx, func() error {
		return adapter.encoder.WriteRadarConfig(sourceID, format, payload)
	})
}

func (adapter *StdoutAdapter) WriteItem(ctx context.Context, item Item) error {
	return adapter.operation(ctx, func() error { return adapter.encoder.WriteItem(item) })
}

func (adapter *StdoutAdapter) EndSource(
	ctx context.Context,
	sourceID string,
	outcome SourceOutcome,
) error {
	return adapter.operation(ctx, func() error { return adapter.encoder.EndSource(sourceID, outcome) })
}

func (adapter *StdoutAdapter) Commit(ctx context.Context, artifact SessionArtifact) error {
	return adapter.terminal(ctx, func() error { return adapter.encoder.Commit(artifact) })
}

func (adapter *StdoutAdapter) Abort(ctx context.Context, reason AbortReason) error {
	return adapter.terminal(ctx, func() error { return adapter.encoder.Abort(reason) })
}

func (adapter *StdoutAdapter) operation(ctx context.Context, emit func() error) error {
	if adapter == nil || adapter.encoder == nil || adapter.output == nil {
		return errors.New("multisensor stream stdout adapter is nil")
	}
	if ctx == nil {
		return errors.Join(errors.New("multisensor stream operation context is nil"), adapter.Close())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, adapter.Close())
	}

	// Arm before locking: a waiting operation's canceled context terminates the
	// whole stream and interrupts whichever operation currently owns Encoder.
	cancellation := armStdoutCancellation(ctx, adapter.output)
	adapter.encoderMu.Lock()
	defer adapter.encoderMu.Unlock()
	if adapter.output.closed.Load() {
		cancellation.stopAndWait()
		return errors.Join(ErrStdoutClosed, ctx.Err(), adapter.output.Close())
	}
	if err := ctx.Err(); err != nil {
		cancellation.stopAndWait()
		return errors.Join(err, adapter.output.Close())
	}

	emitErr := emit()
	canceled := cancellation.stopAndWait()
	if canceled || ctx.Err() != nil {
		return errors.Join(emitErr, ctx.Err(), adapter.output.Close())
	}
	if emitErr != nil {
		// A failed or poisoned Encoder is never resumed and never receives a
		// best-effort terminal append. Physical EOF is the only follow-up.
		return errors.Join(emitErr, adapter.output.Close())
	}
	return nil
}

func (adapter *StdoutAdapter) terminal(ctx context.Context, emit func() error) error {
	if adapter == nil || adapter.encoder == nil || adapter.output == nil {
		return errors.New("multisensor stream stdout adapter is nil")
	}
	if ctx == nil {
		return errors.Join(errors.New("multisensor stream terminal context is nil"), adapter.Close())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, adapter.Close())
	}

	cancellation := armStdoutCancellation(ctx, adapter.output)
	adapter.encoderMu.Lock()
	defer adapter.encoderMu.Unlock()
	if adapter.output.closed.Load() {
		cancellation.stopAndWait()
		return errors.Join(ErrStdoutClosed, ctx.Err(), adapter.output.Close())
	}

	emitErr := error(nil)
	emitted := false
	if ctx.Err() == nil {
		emitErr = emit()
		emitted = emitErr == nil
	}
	canceled := cancellation.stopAndWait()
	closeErr := adapter.output.Close()
	if emitted {
		// A complete COMMIT/ABORT plus explicit EOF is the linearization point;
		// closing the OS file supplies the required physical EOF.
		return closeErr
	}
	if canceled || ctx.Err() != nil {
		return errors.Join(emitErr, ctx.Err(), closeErr)
	}
	return errors.Join(emitErr, closeErr)
}

// Close is idempotent and may race a blocked operation. Closing without a
// terminal record is an aborted stream to every decoder.
func (adapter *StdoutAdapter) Close() error {
	if adapter == nil || adapter.output == nil {
		return errors.New("multisensor stream stdout adapter is nil")
	}
	return adapter.output.Close()
}

type ownedStdoutFile struct {
	file      *os.File
	closeOnce sync.Once
	closed    atomic.Bool
	closeErr  error
}

func (output *ownedStdoutFile) Write(payload []byte) (int, error) {
	if output == nil || output.file == nil {
		return 0, errors.New("multisensor stream stdout is nil")
	}
	if output.closed.Load() {
		return 0, os.ErrClosed
	}
	return output.file.Write(payload)
}

func (output *ownedStdoutFile) Close() error {
	if output == nil || output.file == nil {
		return errors.New("multisensor stream stdout is nil")
	}
	output.closeOnce.Do(func() {
		output.closed.Store(true)
		if err := output.file.Close(); err != nil {
			output.closeErr = fmt.Errorf("close multisensor stream stdout: %w", err)
		}
	})
	return output.closeErr
}

type stdoutCancellation struct {
	stop func() bool
	done chan struct{}
}

func armStdoutCancellation(ctx context.Context, output *ownedStdoutFile) stdoutCancellation {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = output.Close()
		close(done)
	})
	return stdoutCancellation{stop: stop, done: done}
}

func (cancellation stdoutCancellation) stopAndWait() bool {
	if cancellation.stop() {
		return false
	}
	<-cancellation.done
	return true
}

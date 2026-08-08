package capturestream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"mmwcli/internal/radar"
)

// StdoutEncoder owns one OS stdout handle for exactly one finite capture
// stream. Encoder calls are serialized, while Close remains independent so it
// can interrupt a blocked pipe write.
type StdoutEncoder struct {
	encoderMu sync.Mutex
	encoder   *Encoder
	output    *ownedInterruptibleWriter
}

// NewStdoutEncoder transfers exclusive ownership of stdout, including on
// failure. The OS file boundary is intentional: concurrent Close must
// interrupt a blocked child-stdout pipe write on Windows and Linux.
func NewStdoutEncoder(
	ctx context.Context,
	stdout *os.File,
	session Session,
	plan radar.CapturePlan,
	radarConfig []byte,
) (*StdoutEncoder, error) {
	if stdout == nil {
		return nil, errors.New("capture stream stdout is nil")
	}
	return newStdoutEncoder(ctx, stdout, session, plan, radarConfig)
}

// interruptibleWriteCloser is deliberately private. Production passes an
// *os.File; tests may inject a closer whose Close interrupts an active Write.
type interruptibleWriteCloser interface {
	io.Writer
	io.Closer
}

type ownedInterruptibleWriter struct {
	target    interruptibleWriteCloser
	closeOnce sync.Once
	closeErr  error
}

func (output *ownedInterruptibleWriter) Write(payload []byte) (int, error) {
	return output.target.Write(payload)
}

func (output *ownedInterruptibleWriter) Close() error {
	if output == nil {
		return errors.New("capture stream stdout is nil")
	}
	output.closeOnce.Do(func() {
		if err := output.target.Close(); err != nil {
			output.closeErr = fmt.Errorf("close capture stream stdout: %w", err)
		}
	})
	return output.closeErr
}

func newStdoutEncoder(
	ctx context.Context,
	stdout interruptibleWriteCloser,
	session Session,
	plan radar.CapturePlan,
	radarConfig []byte,
) (*StdoutEncoder, error) {
	if stdout == nil {
		return nil, errors.New("capture stream stdout is nil")
	}
	output := &ownedInterruptibleWriter{target: stdout}
	if ctx == nil {
		return nil, errors.Join(
			errors.New("capture stream stdout context is nil"),
			output.Close(),
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, output.Close())
	}

	cancellation := armOutputCancellation(ctx, output)
	if err := ctx.Err(); err != nil {
		cancellation.stopAndWait()
		return nil, errors.Join(err, output.Close())
	}
	encoder, encodeErr := NewEncoder(output, session, plan, radarConfig)
	canceled := cancellation.stopAndWait()
	if canceled || ctx.Err() != nil {
		encodeErr = errors.Join(encodeErr, ctx.Err())
	}
	if encodeErr != nil {
		return nil, errors.Join(encodeErr, output.Close())
	}
	return &StdoutEncoder{encoder: encoder, output: output}, nil
}

// WriteFrame implements FrameSink. A context canceled before ownership is
// acquired causes no stdout Write or Close. Once encoding begins, cancellation
// closes the owned handle to interrupt an already-blocked pipe write.
func (stream *StdoutEncoder) WriteFrame(
	ctx context.Context,
	index uint64,
	payload []byte,
) error {
	if stream == nil {
		return errors.New("capture stream stdout encoder is nil")
	}
	if ctx == nil {
		return errors.New("capture stream frame context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	stream.encoderMu.Lock()
	defer stream.encoderMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	cancellation := armOutputCancellation(ctx, stream.output)
	if err := ctx.Err(); err != nil {
		canceled := cancellation.stopAndWait()
		if canceled {
			return errors.Join(err, stream.output.Close())
		}
		return err
	}

	writeErr := stream.encoder.WriteFrame(index, payload)
	if cancellation.stopAndWait() {
		return errors.Join(writeErr, ctx.Err(), stream.output.Close())
	}
	return writeErr
}

// Commit emits the sole successful terminal record and then closes stdout.
// A complete terminal record is the linearization point even if cancellation
// races with the following Close.
func (stream *StdoutEncoder) Commit(ctx context.Context, artifact Artifact) error {
	return stream.terminal(ctx, func() error { return stream.encoder.Commit(artifact) })
}

// Abort best-effort emits the sole failed terminal record and then closes
// stdout. A poisoned Encoder is never resumed; Close/EOF remains an abort.
func (stream *StdoutEncoder) Abort(ctx context.Context, reason AbortReason) error {
	return stream.terminal(ctx, func() error { return stream.encoder.Abort(reason) })
}

func (stream *StdoutEncoder) terminal(ctx context.Context, emit func() error) error {
	if stream == nil {
		return errors.New("capture stream stdout encoder is nil")
	}
	if ctx == nil {
		return errors.Join(
			errors.New("capture stream terminal context is nil"),
			stream.Close(),
		)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, stream.Close())
	}

	// Arm before locking so a terminal deadline can interrupt a frame that
	// currently owns the Encoder and is blocked in stdout.Write.
	cancellation := armOutputCancellation(ctx, stream.output)
	stream.encoderMu.Lock()
	defer stream.encoderMu.Unlock()

	var emitErr error
	emitted := false
	if ctx.Err() == nil {
		emitErr = emit()
		emitted = emitErr == nil
	}
	canceled := cancellation.stopAndWait()
	closeErr := stream.output.Close()
	if emitted {
		return closeErr
	}
	if canceled || ctx.Err() != nil {
		return errors.Join(emitErr, ctx.Err(), closeErr)
	}
	return errors.Join(emitErr, closeErr)
}

// Close is idempotent and may race any other method. It deliberately does not
// acquire encoderMu because its purpose is to interrupt an active Write.
func (stream *StdoutEncoder) Close() error {
	if stream == nil {
		return errors.New("capture stream stdout encoder is nil")
	}
	return stream.output.Close()
}

type outputCancellation struct {
	stop func() bool
	done chan struct{}
}

func armOutputCancellation(ctx context.Context, output *ownedInterruptibleWriter) outputCancellation {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = output.Close()
		close(done)
	})
	return outputCancellation{stop: stop, done: done}
}

// stopAndWait returns true when cancellation started the Close callback. It
// always joins that callback before the caller may release Encoder ownership.
func (cancellation outputCancellation) stopAndWait() bool {
	if cancellation.stop() {
		return false
	}
	<-cancellation.done
	return true
}

var _ FrameSink = (*StdoutEncoder)(nil)

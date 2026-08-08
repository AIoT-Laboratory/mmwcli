package capturestream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mmwcli/internal/radar"
)

func TestStdoutEncoderTerminalIsFollowedByPipeEOF(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		plan, config := testCapturePlan(t, 1)
		stream, readResult := newPipeStdoutEncoder(t, plan, config)
		frame := make([]byte, int(plan.BytesPerFrame))
		frame[0] = 1
		if err := stream.WriteFrame(context.Background(), 0, frame); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(frame)
		if err := stream.Commit(context.Background(), Artifact{
			sizeBytes: uint64(len(frame)),
			sha256:    digest,
		}); err != nil {
			t.Fatal(err)
		}
		records := decodePipeResult(t, readResult)
		if len(records) != 4 || records[3].kind != recordCommit {
			t.Fatalf("records = %v, want SESSION, CONFIG, FRAME, COMMIT", recordKinds(records))
		}
	})

	t.Run("abort", func(t *testing.T) {
		plan, config := testCapturePlan(t, 2)
		stream, readResult := newPipeStdoutEncoder(t, plan, config)
		frame := make([]byte, int(plan.BytesPerFrame))
		if err := stream.WriteFrame(context.Background(), 0, frame); err != nil {
			t.Fatal(err)
		}
		if err := stream.Abort(context.Background(), AbortCancelled); err != nil {
			t.Fatal(err)
		}
		records := decodePipeResult(t, readResult)
		if len(records) != 4 || records[3].kind != recordAbort {
			t.Fatalf("records = %v, want SESSION, CONFIG, FRAME, ABORT", recordKinds(records))
		}
	})
}

func TestStdoutEncoderPreCanceledOperationsDoNotWrite(t *testing.T) {
	plan, config := testCapturePlan(t, 1)

	t.Run("constructor closes transferred output", func(t *testing.T) {
		output := newRecordingWriteCloser(nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := newStdoutEncoder(ctx, output, testSession(), plan, config); !errors.Is(err, context.Canceled) {
			t.Fatalf("New error = %v, want cancellation", err)
		}
		writes, closes, _ := output.stats()
		if writes != 0 || closes != 1 {
			t.Fatalf("pre-canceled New performed writes=%d closes=%d, want 0/1", writes, closes)
		}
	})

	t.Run("frame has no side effects", func(t *testing.T) {
		output := newRecordingWriteCloser(nil)
		stream, err := newStdoutEncoder(context.Background(), output, testSession(), plan, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		writesBefore, _, bytesBefore := output.stats()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := stream.WriteFrame(ctx, 0, make([]byte, int(plan.BytesPerFrame))); !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteFrame error = %v, want cancellation", err)
		}
		writesAfter, closes, bytesAfter := output.stats()
		if writesAfter != writesBefore || closes != 0 || !bytes.Equal(bytesAfter, bytesBefore) {
			t.Fatalf(
				"pre-canceled frame changed output: writes=%d/%d closes=%d bytes=%d/%d",
				writesBefore,
				writesAfter,
				closes,
				len(bytesBefore),
				len(bytesAfter),
			)
		}
	})
}

func TestStdoutEncoderStopsFrameCancellationCallback(t *testing.T) {
	plan, config := testCapturePlan(t, 1)
	output := newRecordingWriteCloser(nil)
	stream, err := newStdoutEncoder(context.Background(), output, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	frame := make([]byte, int(plan.BytesPerFrame))
	ctx, cancel := context.WithCancel(context.Background())
	if err := stream.WriteFrame(ctx, 0, frame); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-output.closedSignal:
		t.Fatal("completed frame left a cancellation callback that closed stdout")
	case <-time.After(50 * time.Millisecond):
	}
	digest := sha256.Sum256(frame)
	if err := stream.Commit(context.Background(), Artifact{
		sizeBytes: uint64(len(frame)),
		sha256:    digest,
	}); err != nil {
		t.Fatal(err)
	}
	records, err := decodeTestRecords(output.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[3].kind != recordCommit {
		t.Fatalf("records = %v, want a final COMMIT", recordKinds(records))
	}
}

func TestStdoutEncoderCancellationInterruptsBlockedOSPipeWrite(t *testing.T) {
	plan, config := largePipeCapturePlan(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })

	initialRecords := make(chan error, 1)
	go func() {
		for range 2 {
			if err := discardPipeRecord(reader); err != nil {
				initialRecords <- err
				return
			}
		}
		initialRecords <- nil
	}()
	stream, err := NewStdoutEncoder(context.Background(), writer, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if err := awaitError(t, initialRecords, "initial capture stream records"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	frameResult := make(chan error, 1)
	go func() {
		frameResult <- stream.WriteFrame(ctx, 0, make([]byte, int(plan.BytesPerFrame)))
	}()
	select {
	case err := <-frameResult:
		t.Fatalf("multi-megabyte pipe write did not block: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := awaitError(t, frameResult, "canceled pipe write"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFrame error = %v, want cancellation", err)
	}

	remainder := make(chan error, 1)
	go func() {
		_, readErr := io.Copy(io.Discard, reader)
		remainder <- readErr
	}()
	if err := awaitError(t, remainder, "pipe EOF after cancellation"); err != nil {
		t.Fatalf("read canceled pipe: %v", err)
	}
}

func TestStdoutEncoderAbortDeadlineInterruptsBlockedFrame(t *testing.T) {
	plan, config := testCapturePlan(t, 1)
	writeErr := errors.New("blocked frame write failed")
	closeErr := errors.New("stdout close failed")
	output := newBlockingFrameWriteCloser(writeErr, closeErr)
	stream, err := newStdoutEncoder(context.Background(), output, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	frameResult := make(chan error, 1)
	go func() {
		frameResult <- stream.WriteFrame(
			context.Background(),
			0,
			make([]byte, int(plan.BytesPerFrame)),
		)
	}()
	awaitSignal(t, output.entered, "blocked frame write")
	abortContext, cancelAbort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	abortErr := stream.Abort(abortContext, AbortCancelled)
	cancelAbort()
	if !errors.Is(abortErr, context.DeadlineExceeded) || !errors.Is(abortErr, closeErr) {
		t.Fatalf("Abort error = %v, want deadline and close errors", abortErr)
	}
	if err := awaitError(t, frameResult, "interrupted frame write"); !errors.Is(err, writeErr) {
		t.Fatalf("WriteFrame error = %v, want %v", err, writeErr)
	}
	if output.closeCount() != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", output.closeCount())
	}
	records, err := decodeTestRecords(output.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("poisoned stream appended a terminal record: %v", recordKinds(records))
	}
}

func TestStdoutEncoderCloseAndTerminalErrorsAreStable(t *testing.T) {
	t.Run("concurrent close once", func(t *testing.T) {
		plan, config := testCapturePlan(t, 1)
		closeErr := errors.New("close failed")
		output := newRecordingWriteCloser(closeErr)
		stream, err := newStdoutEncoder(context.Background(), output, testSession(), plan, config)
		if err != nil {
			t.Fatal(err)
		}
		const callers = 16
		results := make(chan error, callers)
		var group sync.WaitGroup
		group.Add(callers)
		for range callers {
			go func() {
				defer group.Done()
				results <- stream.Close()
			}()
		}
		group.Wait()
		close(results)
		for err := range results {
			if !errors.Is(err, closeErr) {
				t.Fatalf("Close error = %v, want %v", err, closeErr)
			}
		}
		_, closes, _ := output.stats()
		if closes != 1 {
			t.Fatalf("underlying Close calls = %d, want 1", closes)
		}
	})

	t.Run("terminal joins write and close", func(t *testing.T) {
		plan, config := testCapturePlan(t, 1)
		writeErr := errors.New("terminal write failed")
		closeErr := errors.New("terminal close failed")
		output := newRecordingWriteCloser(closeErr)
		stream, err := newStdoutEncoder(context.Background(), output, testSession(), plan, config)
		if err != nil {
			t.Fatal(err)
		}
		output.failWrites(writeErr)
		err = stream.Abort(context.Background(), AbortCaptureFailed)
		if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
			t.Fatalf("Abort error = %v, want write and close failures", err)
		}
		_, closes, _ := output.stats()
		if closes != 1 {
			t.Fatalf("underlying Close calls = %d, want 1", closes)
		}
	})
}

type pipeReadResult struct {
	wire []byte
	err  error
}

func newPipeStdoutEncoder(
	t *testing.T,
	plan radar.CapturePlan,
	config []byte,
) (*StdoutEncoder, <-chan pipeReadResult) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })
	result := make(chan pipeReadResult, 1)
	go func() {
		wire, readErr := io.ReadAll(reader)
		result <- pipeReadResult{wire: wire, err: readErr}
	}()
	stream, err := NewStdoutEncoder(context.Background(), writer, testSession(), plan, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream, result
}

func decodePipeResult(t *testing.T, result <-chan pipeReadResult) []decodedTestRecord {
	t.Helper()
	select {
	case read := <-result:
		if read.err != nil {
			t.Fatal(read.err)
		}
		records, err := decodeTestRecords(read.wire)
		if err != nil {
			t.Fatal(err)
		}
		return records
	case <-time.After(2 * time.Second):
		t.Fatal("stdout pipe did not reach EOF")
		return nil
	}
}

func recordKinds(records []decodedTestRecord) []recordType {
	kinds := make([]recordType, len(records))
	for index, record := range records {
		kinds[index] = record.kind
	}
	return kinds
}

func largePipeCapturePlan(t *testing.T) (radar.CapturePlan, []byte) {
	t.Helper()
	config := strings.Replace(
		goldenRadarConfig,
		"profileCfg 0 60 7 3 24 0 0 166 1 16 12500 0 0 158",
		"profileCfg 0 60 7 3 24 0 0 166 1 8190 12500 0 0 158",
		1,
	)
	config = strings.Replace(
		config,
		"frameCfg 0 0 1 1 10 1 0",
		"frameCfg 0 0 255 1 10 1 0",
		1,
	)
	plan, err := radar.BuildCaptureSessionV1Plan([]byte(config), radar.FullConfiguration)
	if err != nil {
		t.Fatalf("build large pipe capture plan: %v", err)
	}
	if plan.BytesPerFrame < 4<<20 {
		t.Fatalf("large pipe frame = %d bytes, want at least 4 MiB", plan.BytesPerFrame)
	}
	return plan, []byte(config)
}

func discardPipeRecord(reader io.Reader) error {
	header := make([]byte, RecordHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	payloadBytes := binary.LittleEndian.Uint64(header[32:40])
	if payloadBytes > MaxFramePayloadBytes {
		return errors.New("capture stream test record exceeds payload bound")
	}
	_, err := io.CopyN(io.Discard, reader, int64(payloadBytes))
	return err
}

func awaitError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type recordingWriteCloser struct {
	mu           sync.Mutex
	buffer       bytes.Buffer
	writes       int
	closes       int
	closed       bool
	writeErr     error
	closeErr     error
	closedSignal chan struct{}
}

func newRecordingWriteCloser(closeErr error) *recordingWriteCloser {
	return &recordingWriteCloser{closeErr: closeErr, closedSignal: make(chan struct{})}
}

func (output *recordingWriteCloser) Write(payload []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.closed {
		return 0, os.ErrClosed
	}
	output.writes++
	if output.writeErr != nil {
		return 0, output.writeErr
	}
	return output.buffer.Write(payload)
}

func (output *recordingWriteCloser) Close() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	output.closes++
	if !output.closed {
		output.closed = true
		close(output.closedSignal)
	}
	return output.closeErr
}

func (output *recordingWriteCloser) failWrites(err error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	output.writeErr = err
}

func (output *recordingWriteCloser) stats() (writes, closes int, wire []byte) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.writes, output.closes, bytes.Clone(output.buffer.Bytes())
}

func (output *recordingWriteCloser) bytes() []byte {
	_, _, wire := output.stats()
	return wire
}

type blockingFrameWriteCloser struct {
	mu          sync.Mutex
	buffer      bytes.Buffer
	writes      int
	closes      int
	closed      bool
	writeErr    error
	closeErr    error
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingFrameWriteCloser(writeErr, closeErr error) *blockingFrameWriteCloser {
	return &blockingFrameWriteCloser{
		writeErr: writeErr,
		closeErr: closeErr,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (output *blockingFrameWriteCloser) Write(payload []byte) (int, error) {
	output.mu.Lock()
	output.writes++
	writeNumber := output.writes
	if writeNumber <= 4 {
		written, err := output.buffer.Write(payload)
		output.mu.Unlock()
		return written, err
	}
	output.enterOnce.Do(func() { close(output.entered) })
	release := output.release
	output.mu.Unlock()
	<-release
	return 0, output.writeErr
}

func (output *blockingFrameWriteCloser) Close() error {
	output.mu.Lock()
	output.closes++
	output.closed = true
	output.releaseOnce.Do(func() { close(output.release) })
	err := output.closeErr
	output.mu.Unlock()
	return err
}

func (output *blockingFrameWriteCloser) closeCount() int {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.closes
}

func (output *blockingFrameWriteCloser) bytes() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return bytes.Clone(output.buffer.Bytes())
}

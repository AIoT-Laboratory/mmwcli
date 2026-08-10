package capturestream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
)

type frameSinkFunc func(ctx context.Context, index uint64, payload []byte) error

func (function frameSinkFunc) WriteFrame(ctx context.Context, index uint64, payload []byte) error {
	return function(ctx, index, payload)
}

func TestMirrorWritesAuthorityBeforeOrderedOwnedFrames(t *testing.T) {
	output := newMirrorTestOutput(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var framesMu sync.Mutex
	var indexes []uint64
	var frames [][]byte
	sink := frameSinkFunc(func(_ context.Context, index uint64, payload []byte) error {
		enteredOnce.Do(func() { close(entered) })
		<-release
		onDisk, err := os.ReadFile(output.PartPath())
		if err != nil {
			return fmt.Errorf("read authoritative part: %w", err)
		}
		start := int(index) * 8
		end := start + len(payload)
		if end > len(onDisk) || !bytes.Equal(onDisk[start:end], payload) {
			return fmt.Errorf("frame %d reached sink before its authoritative bytes", index)
		}
		framesMu.Lock()
		indexes = append(indexes, index)
		frames = append(frames, bytes.Clone(payload))
		framesMu.Unlock()
		return nil
	})
	var canceled atomic.Int32
	mirror, err := NewMirror(
		output,
		sink,
		func() { canceled.Add(1) },
		MirrorConfig{FrameBytes: 8, FrameCount: 2, BufferBytes: 16},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mirror.Abort)

	second := []byte{8, 9, 10, 11, 12, 13, 14, 15}
	if _, err := mirror.WriteAt(second, 8); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("frame 1 was emitted before frame 0 completed")
	default:
	}
	tail := []byte{6, 7}
	if _, err := mirror.WriteAt(tail, 6); err != nil {
		t.Fatal(err)
	}
	head := []byte{0, 1, 2, 3, 4, 5}
	if _, err := mirror.WriteAt(head, 0); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, entered, "first provisional frame")
	for index := range second {
		second[index] = 0xff
	}
	for index := range tail {
		tail[index] = 0xff
	}
	for index := range head {
		head[index] = 0xff
	}
	close(release)

	sealMirror(t, mirror)
	framesMu.Lock()
	defer framesMu.Unlock()
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("frame indexes = %v, want [0 1]", indexes)
	}
	want := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	if !bytes.Equal(frames[0], want[:8]) || !bytes.Equal(frames[1], want[8:]) {
		t.Fatalf("owned frames = %v, want %v", frames, want)
	}
	if canceled.Load() != 0 {
		t.Fatalf("successful mirror called cancel %d time(s)", canceled.Load())
	}
	if output.Committed() {
		t.Fatal("mirror published the authoritative output")
	}
	if _, err := os.Stat(output.FinalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final capture exists before outer commit: %v", err)
	}
}

func TestMirrorSplitsCrossFrameWriterAtFragments(t *testing.T) {
	output := newMirrorTestOutput(t)
	var framesMu sync.Mutex
	var frames [][]byte
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(_ context.Context, _ uint64, payload []byte) error {
			framesMu.Lock()
			frames = append(frames, bytes.Clone(payload))
			framesMu.Unlock()
			return nil
		}),
		func() {},
		MirrorConfig{FrameBytes: 8, FrameCount: 2, BufferBytes: 16},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mirror.Abort)

	if _, err := mirror.WriteAt([]byte{6, 7, 8, 9}, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3, 4, 5}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.WriteAt([]byte{10, 11, 12, 13, 14, 15}, 10); err != nil {
		t.Fatal(err)
	}
	sealMirror(t, mirror)

	framesMu.Lock()
	defer framesMu.Unlock()
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2", len(frames))
	}
	if !bytes.Equal(frames[0], []byte{0, 1, 2, 3, 4, 5, 6, 7}) ||
		!bytes.Equal(frames[1], []byte{8, 9, 10, 11, 12, 13, 14, 15}) {
		t.Fatalf("frames = %v", frames)
	}
}

func TestOpenEndedMirrorSealsObservedWholeFrames(t *testing.T) {
	output := newMirrorTestOutput(t)
	var framesMu sync.Mutex
	var frames [][]byte
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(_ context.Context, _ uint64, payload []byte) error {
			framesMu.Lock()
			frames = append(frames, bytes.Clone(payload))
			framesMu.Unlock()
			return nil
		}),
		func() {},
		BoundedOpenEndedMirrorConfig(4, 12),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mirror.Abort)

	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3, 4, 5, 6, 7}, 0); err != nil {
		t.Fatal(err)
	}
	sealMirror(t, mirror)

	framesMu.Lock()
	defer framesMu.Unlock()
	if len(frames) != 2 ||
		!bytes.Equal(frames[0], []byte{0, 1, 2, 3}) ||
		!bytes.Equal(frames[1], []byte{4, 5, 6, 7}) {
		t.Fatalf("open-ended frames = %v", frames)
	}
}

func TestOpenEndedMirrorRejectsEmptyPartialAndOverLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		write  []byte
		offset int64
		seal   bool
	}{
		{name: "empty", seal: true},
		{name: "partial", write: []byte{0, 1, 2, 3, 4, 5}, seal: true},
		{name: "over limit", write: []byte{8, 9, 10, 11}, offset: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := newMirrorTestOutput(t)
			mirror, err := NewMirror(
				output,
				frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return nil }),
				func() {},
				BoundedOpenEndedMirrorConfig(4, 8),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(mirror.Abort)
			if len(test.write) != 0 {
				_, err = mirror.WriteAt(test.write, test.offset)
			}
			if err == nil && test.seal {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err = mirror.Seal(ctx)
			}
			if err == nil || !errors.Is(err, ErrMirrorIntegrity) {
				t.Fatalf("open-ended %s error = %v", test.name, err)
			}
		})
	}
}

func TestMirrorSlowSinkFailsFastAtBoundedBuffer(t *testing.T) {
	output := newMirrorTestOutput(t)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	canceled := make(chan struct{})
	var cancelOnce sync.Once
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(ctx context.Context, _ uint64, _ []byte) error {
			enteredOnce.Do(func() { close(entered) })
			<-ctx.Done()
			return io.ErrClosedPipe
		}),
		func() { cancelOnce.Do(func() { close(canceled) }) },
		MirrorConfig{FrameBytes: 4, FrameCount: 3, BufferBytes: 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mirror.Abort)

	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, entered, "blocked sink")
	if _, err := mirror.WriteAt([]byte{4, 5, 6, 7}, 4); err != nil {
		t.Fatal(err)
	}
	type writeResult struct {
		count int
		err   error
	}
	result := make(chan writeResult, 1)
	go func() {
		count, writeErr := mirror.WriteAt([]byte{8, 9, 10, 11}, 8)
		result <- writeResult{count: count, err: writeErr}
	}()
	select {
	case got := <-result:
		if got.count != 0 || !errors.Is(got.err, ErrMirrorBackpressure) {
			t.Fatalf("third write = (%d, %v), want (0, backpressure)", got.count, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriterAt blocked on the slow sink")
	}
	waitForSignal(t, canceled, "capture cancellation")
	onDisk, err := os.ReadFile(output.PartPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, []byte{0, 1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("authoritative bytes after rejected write = %v", onDisk)
	}
	waitForSignal(t, mirror.done, "mirror worker shutdown")
	if err := mirror.Err(); !errors.Is(err, ErrMirrorBackpressure) {
		t.Fatalf("mirror error = %v, want %v", err, ErrMirrorBackpressure)
	}
}

func TestMirrorPropagatesSinkFailureAndStopsAuthority(t *testing.T) {
	output := newMirrorTestOutput(t)
	wantErr := errors.New("sink failed")
	canceled := make(chan struct{})
	var cancelOnce sync.Once
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return wantErr }),
		func() { cancelOnce.Do(func() { close(canceled) }) },
		MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mirror.Abort)

	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, canceled, "sink failure cancellation")
	waitForSignal(t, mirror.done, "mirror worker shutdown")
	if count, err := mirror.WriteAt([]byte{4, 5, 6, 7}, 4); count != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("write after sink failure = (%d, %v), want (0, %v)", count, err, wantErr)
	}
	onDisk, err := os.ReadFile(output.PartPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, []byte{0, 1, 2, 3}) {
		t.Fatalf("authoritative bytes after sink failure = %v", onDisk)
	}
	if err := mirror.Err(); !errors.Is(err, wantErr) {
		t.Fatalf("mirror error = %v, want %v", err, wantErr)
	}
}

func TestMirrorPropagatesAuthoritativeOutputFailure(t *testing.T) {
	output := newMirrorTestOutput(t)
	var sinkCalls atomic.Int32
	canceled := make(chan struct{})
	var cancelOnce sync.Once
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error {
			sinkCalls.Add(1)
			return nil
		}),
		func() { cancelOnce.Do(func() { close(canceled) }) },
		MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if count, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); count != 0 || err == nil {
		t.Fatalf("write to closed authority = (%d, %v), want an error", count, err)
	}
	waitForSignal(t, canceled, "authoritative failure cancellation")
	waitForSignal(t, mirror.done, "mirror worker shutdown")
	if mirror.Err() == nil {
		t.Fatal("authoritative failure was not retained")
	}
	if sinkCalls.Load() != 0 {
		t.Fatalf("sink calls = %d, want 0", sinkCalls.Load())
	}
}

func TestMirrorRejectsInvalidWritesBeforeAuthority(t *testing.T) {
	tests := []struct {
		name          string
		config        MirrorConfig
		write         func(*Mirror) (int, error)
		wantError     error
		wantAuthority []byte
	}{
		{
			name:      "empty",
			config:    MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 8},
			write:     func(mirror *Mirror) (int, error) { return mirror.WriteAt(nil, 0) },
			wantError: ErrMirrorIntegrity,
		},
		{
			name:      "negative offset",
			config:    MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 8},
			write:     func(mirror *Mirror) (int, error) { return mirror.WriteAt([]byte{1}, -1) },
			wantError: ErrMirrorIntegrity,
		},
		{
			name:      "past expected bytes",
			config:    MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 8},
			write:     func(mirror *Mirror) (int, error) { return mirror.WriteAt([]byte{1, 2}, 7) },
			wantError: ErrMirrorIntegrity,
		},
		{
			name:      "outside reorder window",
			config:    MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 4},
			write:     func(mirror *Mirror) (int, error) { return mirror.WriteAt([]byte{4, 5, 6, 7}, 4) },
			wantError: ErrMirrorBackpressure,
		},
		{
			name:   "overlap",
			config: MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 8},
			write: func(mirror *Mirror) (int, error) {
				if _, err := mirror.WriteAt([]byte{0, 1}, 0); err != nil {
					return 0, err
				}
				return mirror.WriteAt([]byte{9, 9}, 1)
			},
			wantError:     ErrMirrorIntegrity,
			wantAuthority: []byte{0, 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := newMirrorTestOutput(t)
			canceled := make(chan struct{})
			var cancelOnce sync.Once
			mirror, err := NewMirror(
				output,
				frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return nil }),
				func() { cancelOnce.Do(func() { close(canceled) }) },
				test.config,
			)
			if err != nil {
				t.Fatal(err)
			}
			count, err := test.write(mirror)
			if count != 0 || !errors.Is(err, test.wantError) {
				t.Fatalf("invalid write = (%d, %v), want (0, %v)", count, err, test.wantError)
			}
			waitForSignal(t, canceled, "invalid write cancellation")
			onDisk, err := os.ReadFile(output.PartPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(onDisk, test.wantAuthority) {
				t.Fatalf("authoritative bytes = %v, want %v", onDisk, test.wantAuthority)
			}
			waitForSignal(t, mirror.done, "mirror worker shutdown")
			if err := mirror.Err(); !errors.Is(err, test.wantError) {
				t.Fatalf("mirror error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestMirrorSealRejectsIncompleteCapture(t *testing.T) {
	output := newMirrorTestOutput(t)
	canceled := make(chan struct{})
	var cancelOnce sync.Once
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return nil }),
		func() { cancelOnce.Do(func() { close(canceled) }) },
		MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.WriteAt([]byte{0, 1}, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mirror.Seal(ctx); !errors.Is(err, ErrMirrorIntegrity) {
		t.Fatalf("Seal error = %v, want %v", err, ErrMirrorIntegrity)
	}
	waitForSignal(t, canceled, "incomplete capture cancellation")
	waitForSignal(t, mirror.done, "mirror worker shutdown")
	if output.Committed() {
		t.Fatal("incomplete mirror published its output")
	}
}

func TestMirrorSealDeadlineDoesNotJoinBlockedSink(t *testing.T) {
	output := newMirrorTestOutput(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(ctx context.Context, _ uint64, _ []byte) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
		func() {},
		MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, entered, "blocked sink")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	started := time.Now()
	err = mirror.Seal(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("Seal error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		close(release)
		t.Fatalf("Seal waited %v for blocked sink", elapsed)
	}
	close(release)
	waitForSignal(t, mirror.done, "mirror worker shutdown")
}

func TestMirrorAbortCancelsBlockedSinkWithoutJoining(t *testing.T) {
	output := newMirrorTestOutput(t)
	entered := make(chan struct{})
	observedCancel := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSink := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSink)
	var sinkCalls atomic.Int32
	var sideEffects atomic.Int32
	var captureCancels atomic.Int32
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(ctx context.Context, _ uint64, _ []byte) error {
			sinkCalls.Add(1)
			close(entered)
			<-ctx.Done()
			close(observedCancel)
			<-release
			if ctx.Err() == nil {
				sideEffects.Add(1)
			}
			return io.ErrClosedPipe
		}),
		func() { captureCancels.Add(1) },
		MirrorConfig{FrameBytes: 4, FrameCount: 2, BufferBytes: 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mirror.Abort)
	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, entered, "blocked sink")
	if _, err := mirror.WriteAt([]byte{4, 5, 6, 7}, 4); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	mirror.Abort()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Abort joined blocked sink for %v", elapsed)
	}
	waitForSignal(t, observedCancel, "sink context cancellation")
	select {
	case <-mirror.done:
		t.Fatal("worker exited before the admitted sink call returned")
	default:
	}
	if err := mirror.Err(); !errors.Is(err, ErrMirrorAborted) {
		t.Fatalf("mirror error = %v, want %v", err, ErrMirrorAborted)
	}
	releaseSink()
	waitForSignal(t, mirror.done, "mirror worker shutdown")
	if err := mirror.Err(); !errors.Is(err, ErrMirrorAborted) {
		t.Fatalf("post-worker mirror error = %v, want %v", err, ErrMirrorAborted)
	}
	mirror.mu.Lock()
	allocated := mirror.allocated
	mirror.mu.Unlock()
	if allocated != 0 {
		t.Fatalf("allocated frame buffers = %d, want 0", allocated)
	}
	if sinkCalls.Load() != 1 {
		t.Fatalf("sink calls = %d, want only the pre-Abort call", sinkCalls.Load())
	}
	if sideEffects.Load() != 0 {
		t.Fatalf("side effects after cancellation = %d", sideEffects.Load())
	}
	if captureCancels.Load() != 1 {
		t.Fatalf("capture cancellations = %d, want 1", captureCancels.Load())
	}
}

func TestMirrorAbortWinsConcurrentSeal(t *testing.T) {
	output := newMirrorTestOutput(t)
	entered := make(chan struct{})
	var captureCancels atomic.Int32
	mirror, err := NewMirror(
		output,
		frameSinkFunc(func(ctx context.Context, _ uint64, _ []byte) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}),
		func() { captureCancels.Add(1) },
		MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, entered, "blocked sink")

	sealResult := make(chan error, 1)
	sealContext, cancelSeal := context.WithTimeout(context.Background(), time.Second)
	defer cancelSeal()
	go func() { sealResult <- mirror.Seal(sealContext) }()
	waitForMirrorState(t, mirror, mirrorSealing)
	mirror.Abort()
	select {
	case err := <-sealResult:
		if !errors.Is(err, ErrMirrorAborted) {
			t.Fatalf("concurrent Seal error = %v, want %v", err, ErrMirrorAborted)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Seal did not observe Abort")
	}
	waitForSignal(t, mirror.done, "mirror worker shutdown")
	if err := mirror.Err(); !errors.Is(err, ErrMirrorAborted) {
		t.Fatalf("mirror error = %v, want %v", err, ErrMirrorAborted)
	}
	if captureCancels.Load() != 1 {
		t.Fatalf("capture cancellations = %d, want 1", captureCancels.Load())
	}
}

func TestMirrorSealAndAbortStateTransitions(t *testing.T) {
	t.Run("sealed", func(t *testing.T) {
		output := newMirrorTestOutput(t)
		var canceled atomic.Int32
		mirror, err := NewMirror(
			output,
			frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return nil }),
			func() { canceled.Add(1) },
			MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mirror.WriteAt([]byte{0, 1, 2, 3}, 0); err != nil {
			t.Fatal(err)
		}
		sealMirror(t, mirror)
		sealMirror(t, mirror)
		if count, err := mirror.WriteAt([]byte{0}, 0); count != 0 || !errors.Is(err, ErrMirrorSealed) {
			t.Fatalf("write after Seal = (%d, %v)", count, err)
		}
		mirror.Abort()
		if canceled.Load() != 0 {
			t.Fatalf("Abort after Seal canceled capture %d time(s)", canceled.Load())
		}
		if output.Committed() {
			t.Fatal("Seal published the authoritative output")
		}
	})

	t.Run("aborted", func(t *testing.T) {
		output := newMirrorTestOutput(t)
		var canceled atomic.Int32
		mirror, err := NewMirror(
			output,
			frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return nil }),
			func() { canceled.Add(1) },
			MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4},
		)
		if err != nil {
			t.Fatal(err)
		}
		mirror.Abort()
		mirror.Abort()
		waitForSignal(t, mirror.done, "mirror worker shutdown")
		if canceled.Load() != 1 {
			t.Fatalf("Abort canceled capture %d time(s), want 1", canceled.Load())
		}
		if count, err := mirror.WriteAt([]byte{0}, 0); count != 0 || !errors.Is(err, ErrMirrorAborted) {
			t.Fatalf("write after Abort = (%d, %v)", count, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mirror.Seal(ctx); !errors.Is(err, ErrMirrorAborted) {
			t.Fatalf("Seal after Abort error = %v", err)
		}
	})
}

func TestNewMirrorValidatesResourceBounds(t *testing.T) {
	output := newMirrorTestOutput(t)
	sink := frameSinkFunc(func(_ context.Context, _ uint64, _ []byte) error { return nil })
	cancel := context.CancelFunc(func() {})
	valid := MirrorConfig{FrameBytes: 4, FrameCount: 1, BufferBytes: 4}
	if _, err := NewMirror(nil, sink, cancel, valid); err == nil {
		t.Fatal("NewMirror accepted a nil output")
	}
	if _, err := NewMirror(output, nil, cancel, valid); err == nil {
		t.Fatal("NewMirror accepted a nil sink")
	}
	if _, err := NewMirror(output, sink, nil, valid); err == nil {
		t.Fatal("NewMirror accepted a nil cancel function")
	}
	invalid := []MirrorConfig{
		{FrameBytes: 0, FrameCount: 1, BufferBytes: 4},
		{FrameBytes: 3, FrameCount: 1, BufferBytes: 4},
		{FrameBytes: int64(MaxFramePayloadBytes) + 2, FrameCount: 1, BufferBytes: int64(MaxFramePayloadBytes) + 2},
		{FrameBytes: 4, FrameCount: 0, BufferBytes: 4},
		{FrameBytes: 4, FrameCount: ^uint64(0), BufferBytes: 4},
		{FrameBytes: 4, FrameCount: 1, BufferBytes: 2},
		{FrameBytes: 4, FrameCount: 1, BufferBytes: MaxMirrorBufferBytes + 1},
	}
	for _, config := range invalid {
		if mirror, err := NewMirror(output, sink, cancel, config); err == nil {
			mirror.Abort()
			t.Fatalf("NewMirror accepted invalid config %+v", config)
		}
	}

	closed := newMirrorTestOutput(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMirror(closed, sink, cancel, valid); err == nil {
		t.Fatal("NewMirror accepted a closed output")
	}

	config := DefaultMirrorConfig(32<<20, 2)
	if config.BufferBytes != 32<<20 {
		t.Fatalf("large-frame default buffer = %d, want %d", config.BufferBytes, int64(32<<20))
	}
}

func newMirrorTestOutput(t *testing.T) *capturefile.File {
	t.Helper()
	output, err := capturefile.Create(filepath.Join(t.TempDir(), "capture.bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Errorf("close capture output: %v", err)
		}
	})
	return output
}

func sealMirror(t *testing.T, mirror *Mirror) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mirror.Seal(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForMirrorState(t *testing.T, mirror *Mirror, want mirrorState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mirror.mu.Lock()
		state := mirror.state
		mirror.mu.Unlock()
		if state == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for mirror state %d", want)
}

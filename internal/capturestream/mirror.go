package capturestream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"mmwcli/internal/capturefile"
)

const (
	DefaultMirrorBufferBytes int64 = 16 << 20
	MaxMirrorBufferBytes     int64 = 128 << 20
	maxMirrorFrameSlots            = 4096
)

var (
	ErrMirrorIntegrity    = errors.New("capture stream mirror integrity failure")
	ErrMirrorBackpressure = errors.New("capture stream mirror backpressure")
	ErrMirrorSealed       = errors.New("capture stream mirror is sealed")
	ErrMirrorAborted      = errors.New("capture stream mirror is aborted")
)

// FrameSink consumes one owned, complete frame synchronously. It must check
// ctx before external side effects, stop promptly after cancellation, and not
// retain or mutate payload after WriteFrame returns.
type FrameSink interface {
	WriteFrame(ctx context.Context, index uint64, payload []byte) error
}

type MirrorConfig struct {
	FrameBytes  int64
	FrameCount  uint64
	BufferBytes int64
}

func DefaultMirrorConfig(frameBytes int64, frameCount uint64) MirrorConfig {
	bufferBytes := DefaultMirrorBufferBytes
	if frameBytes > bufferBytes {
		bufferBytes = frameBytes
	}
	return MirrorConfig{
		FrameBytes:  frameBytes,
		FrameCount:  frameCount,
		BufferBytes: bufferBytes,
	}
}

type mirrorState uint8

const (
	mirrorOpen mirrorState = iota
	mirrorSealing
	mirrorSealed
	mirrorAborted
)

type queuedFrame struct {
	index   uint64
	payload []byte
}

type mirrorFragment struct {
	frame        *frameSpool
	frameOffset  int64
	payloadStart int
	payloadEnd   int
}

// Mirror writes every fragment to the authoritative capture output before it
// updates provisional frame coverage. A bounded worker isolates FrameSink from
// the DCA WriterAt path. Mirror never truncates, commits, closes, or publishes
// the wrapped output.
type Mirror struct {
	output      capturefile.Output
	sink        FrameSink
	cancel      context.CancelFunc
	config      MirrorConfig
	sinkContext context.Context
	sinkCancel  context.CancelFunc

	expectedBytes int64
	maxSlots      int
	queue         chan queuedFrame
	done          chan struct{}

	mu          sync.Mutex
	state       mirrorState
	failure     error
	slots       map[uint64]*frameSpool
	allocated   int
	nextEmit    uint64
	delivered   uint64
	queueClosed bool
	cancelOnce  sync.Once
}

func NewMirror(
	output capturefile.Output,
	sink FrameSink,
	cancel context.CancelFunc,
	config MirrorConfig,
) (*Mirror, error) {
	if !capturefile.IsUsableOutput(output) {
		return nil, errors.New("capture stream mirror output is unavailable")
	}
	if sink == nil {
		return nil, errors.New("capture stream mirror sink is nil")
	}
	if cancel == nil {
		return nil, errors.New("capture stream mirror cancel function is nil")
	}
	if config.FrameBytes <= 0 || config.FrameBytes%2 != 0 {
		return nil, errors.New("capture stream mirror frame size must be positive and int16-aligned")
	}
	if config.FrameBytes > int64(MaxFramePayloadBytes) {
		return nil, fmt.Errorf(
			"capture stream mirror frame size %d exceeds limit %d",
			config.FrameBytes,
			MaxFramePayloadBytes,
		)
	}
	if config.FrameCount == 0 {
		return nil, errors.New("capture stream mirror frame count must be positive")
	}
	if config.FrameCount > uint64(math.MaxInt64/config.FrameBytes) {
		return nil, errors.New("capture stream mirror expected byte count exceeds int64")
	}
	if config.BufferBytes < config.FrameBytes {
		return nil, fmt.Errorf(
			"capture stream mirror buffer %d is smaller than one frame of %d bytes",
			config.BufferBytes,
			config.FrameBytes,
		)
	}
	if config.BufferBytes > MaxMirrorBufferBytes {
		return nil, fmt.Errorf(
			"capture stream mirror buffer %d exceeds hard limit %d",
			config.BufferBytes,
			MaxMirrorBufferBytes,
		)
	}

	maxSlots := config.BufferBytes / config.FrameBytes
	if maxSlots > maxMirrorFrameSlots {
		maxSlots = maxMirrorFrameSlots
	}
	if maxSlots > int64(config.FrameCount) {
		maxSlots = int64(config.FrameCount)
	}
	if maxSlots <= 0 {
		return nil, errors.New("capture stream mirror has no frame slots")
	}
	sinkContext, sinkCancel := context.WithCancel(context.Background())
	mirror := &Mirror{
		output:        output,
		sink:          sink,
		cancel:        cancel,
		config:        config,
		sinkContext:   sinkContext,
		sinkCancel:    sinkCancel,
		expectedBytes: int64(config.FrameCount) * config.FrameBytes,
		maxSlots:      int(maxSlots),
		queue:         make(chan queuedFrame, int(maxSlots)),
		done:          make(chan struct{}),
		slots:         make(map[uint64]*frameSpool),
	}
	go mirror.runSink()
	return mirror, nil
}

func (mirror *Mirror) WriteAt(payload []byte, offset int64) (int, error) {
	if mirror == nil {
		return 0, errors.New("capture stream mirror is nil")
	}

	mirror.mu.Lock()
	if err := mirror.writableErrorLocked(); err != nil {
		mirror.mu.Unlock()
		return 0, err
	}
	fragments, newSlots, err := mirror.planWriteLocked(payload, offset)
	if err != nil {
		first := mirror.failLocked(err)
		mirror.mu.Unlock()
		if first {
			mirror.sinkCancel()
			mirror.cancelCapture()
		}
		return 0, err
	}

	written, writeErr := mirror.output.WriteAt(payload, offset)
	if writeErr == nil && written != len(payload) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		err = fmt.Errorf("write authoritative capture output at offset %d: %w", offset, writeErr)
		first := mirror.failLocked(err)
		mirror.mu.Unlock()
		if first {
			mirror.sinkCancel()
			mirror.cancelCapture()
		}
		return written, err
	}

	for index, frame := range newSlots {
		mirror.slots[index] = frame
		mirror.allocated++
	}
	for _, fragment := range fragments {
		fragment.frame.apply(
			fragment.frameOffset,
			payload[fragment.payloadStart:fragment.payloadEnd],
		)
	}
	if err := mirror.enqueueCompleteLocked(); err != nil {
		first := mirror.failLocked(err)
		mirror.mu.Unlock()
		if first {
			mirror.sinkCancel()
			mirror.cancelCapture()
		}
		return len(payload), err
	}
	mirror.mu.Unlock()
	return len(payload), nil
}

// Seal stops accepting writes and waits for every exact frame to finish at the
// sink. A deadline is mandatory so a blocked sink cannot hold capture cleanup.
func (mirror *Mirror) Seal(ctx context.Context) error {
	if mirror == nil {
		return errors.New("capture stream mirror is nil")
	}
	if ctx == nil {
		return errors.New("capture stream mirror seal context is nil")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("capture stream mirror seal requires a deadline")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	mirror.mu.Lock()
	switch mirror.state {
	case mirrorSealed:
		mirror.mu.Unlock()
		return nil
	case mirrorAborted:
		err := mirror.abortedErrorLocked()
		mirror.mu.Unlock()
		return err
	case mirrorOpen:
		if mirror.failure != nil {
			err := mirror.failure
			mirror.abortLocked()
			mirror.mu.Unlock()
			mirror.sinkCancel()
			mirror.cancelCapture()
			return err
		}
		if mirror.nextEmit != mirror.config.FrameCount || len(mirror.slots) != 0 {
			err := fmt.Errorf(
				"%w: emitted=%d expected=%d incompleteFrames=%d",
				ErrMirrorIntegrity,
				mirror.nextEmit,
				mirror.config.FrameCount,
				len(mirror.slots),
			)
			mirror.failLocked(err)
			mirror.mu.Unlock()
			mirror.sinkCancel()
			mirror.cancelCapture()
			return err
		}
		mirror.state = mirrorSealing
		mirror.closeQueueLocked()
	case mirrorSealing:
		// Another bounded Seal call may continue waiting for the same worker.
	default:
		mirror.mu.Unlock()
		return errors.New("capture stream mirror has invalid state")
	}
	done := mirror.done
	mirror.mu.Unlock()

	select {
	case <-done:
		mirror.mu.Lock()
		switch mirror.state {
		case mirrorAborted:
			err := mirror.abortedErrorLocked()
			mirror.mu.Unlock()
			return err
		case mirrorSealed:
			mirror.mu.Unlock()
			return nil
		case mirrorSealing:
			// Validate the completed worker below.
		default:
			mirror.mu.Unlock()
			return errors.New("capture stream mirror has invalid terminal state")
		}
		if mirror.failure != nil {
			err := mirror.failure
			mirror.state = mirrorAborted
			mirror.mu.Unlock()
			mirror.sinkCancel()
			return err
		}
		if mirror.delivered != mirror.config.FrameCount || mirror.allocated != 0 {
			err := fmt.Errorf(
				"%w: delivered=%d expected=%d allocated=%d",
				ErrMirrorIntegrity,
				mirror.delivered,
				mirror.config.FrameCount,
				mirror.allocated,
			)
			mirror.failLocked(err)
			mirror.mu.Unlock()
			mirror.sinkCancel()
			mirror.cancelCapture()
			return err
		}
		mirror.state = mirrorSealed
		mirror.mu.Unlock()
		mirror.sinkCancel()
		return nil
	case <-ctx.Done():
		err := fmt.Errorf("seal capture stream mirror: %w", ctx.Err())
		mirror.mu.Lock()
		if mirror.state == mirrorSealed {
			mirror.mu.Unlock()
			return nil
		}
		if mirror.state == mirrorAborted {
			err = mirror.abortedErrorLocked()
			mirror.mu.Unlock()
			return err
		}
		mirror.failLocked(err)
		mirror.mu.Unlock()
		mirror.sinkCancel()
		mirror.cancelCapture()
		return err
	}
}

// Abort stops accepting writes, releases active spool buffers, and closes the
// worker queue without waiting for a sink that may be blocked.
func (mirror *Mirror) Abort() {
	if mirror == nil {
		return
	}
	mirror.mu.Lock()
	if mirror.state == mirrorSealed {
		mirror.mu.Unlock()
		return
	}
	if mirror.state == mirrorAborted {
		mirror.mu.Unlock()
		mirror.sinkCancel()
		mirror.cancelCapture()
		return
	}
	mirror.abortLocked()
	mirror.mu.Unlock()
	mirror.sinkCancel()
	mirror.cancelCapture()
}

func (mirror *Mirror) Err() error {
	if mirror == nil {
		return errors.New("capture stream mirror is nil")
	}
	mirror.mu.Lock()
	defer mirror.mu.Unlock()
	if mirror.failure != nil {
		return mirror.failure
	}
	if mirror.state == mirrorAborted {
		return ErrMirrorAborted
	}
	return nil
}

func (mirror *Mirror) planWriteLocked(
	payload []byte,
	offset int64,
) ([]mirrorFragment, map[uint64]*frameSpool, error) {
	if len(payload) == 0 {
		return nil, nil, fmt.Errorf("%w: empty WriterAt payload", ErrMirrorIntegrity)
	}
	if offset < 0 || offset > mirror.expectedBytes {
		return nil, nil, fmt.Errorf(
			"%w: offset %d outside [0,%d)",
			ErrMirrorIntegrity,
			offset,
			mirror.expectedBytes,
		)
	}
	length := int64(len(payload))
	if length > mirror.expectedBytes-offset {
		return nil, nil, fmt.Errorf(
			"%w: write [%d,%d) exceeds expected %d bytes",
			ErrMirrorIntegrity,
			offset,
			offset+length,
			mirror.expectedBytes,
		)
	}
	end := offset + length
	firstFrame := uint64(offset / mirror.config.FrameBytes)
	lastFrame := uint64((end - 1) / mirror.config.FrameBytes)
	if firstFrame < mirror.nextEmit {
		return nil, nil, fmt.Errorf(
			"%w: write starts in already emitted frame %d",
			ErrMirrorIntegrity,
			firstFrame,
		)
	}
	if lastFrame-mirror.nextEmit >= uint64(mirror.maxSlots) {
		return nil, nil, fmt.Errorf(
			"%w: frame %d exceeds reorder window [%d,%d)",
			ErrMirrorBackpressure,
			lastFrame,
			mirror.nextEmit,
			mirror.nextEmit+uint64(mirror.maxSlots),
		)
	}

	missing := 0
	for index := firstFrame; index <= lastFrame; index++ {
		if mirror.slots[index] == nil {
			missing++
		}
	}
	if missing > mirror.maxSlots-mirror.allocated {
		return nil, nil, fmt.Errorf(
			"%w: need %d frame slot(s), %d available",
			ErrMirrorBackpressure,
			missing,
			mirror.maxSlots-mirror.allocated,
		)
	}

	newSlots := make(map[uint64]*frameSpool, missing)
	fragments := make([]mirrorFragment, 0, int(lastFrame-firstFrame+1))
	for index := firstFrame; index <= lastFrame; index++ {
		frame := mirror.slots[index]
		if frame == nil {
			frame = newFrameSpool(index, mirror.config.FrameBytes)
			newSlots[index] = frame
		}
		frameStart := int64(index) * mirror.config.FrameBytes
		fragmentStart := max(offset, frameStart)
		fragmentEnd := min(end, frameStart+mirror.config.FrameBytes)
		frameOffset := fragmentStart - frameStart
		if err := frame.preflight(frameOffset, frameOffset+fragmentEnd-fragmentStart); err != nil {
			return nil, nil, err
		}
		fragments = append(fragments, mirrorFragment{
			frame:        frame,
			frameOffset:  frameOffset,
			payloadStart: int(fragmentStart - offset),
			payloadEnd:   int(fragmentEnd - offset),
		})
	}
	return fragments, newSlots, nil
}

func (mirror *Mirror) enqueueCompleteLocked() error {
	for mirror.nextEmit < mirror.config.FrameCount {
		frame := mirror.slots[mirror.nextEmit]
		if frame == nil || !frame.complete() {
			return nil
		}
		queued := queuedFrame{index: mirror.nextEmit, payload: frame.data}
		select {
		case mirror.queue <- queued:
			delete(mirror.slots, mirror.nextEmit)
			mirror.nextEmit++
		default:
			return fmt.Errorf(
				"%w: encoder queue is full at frame %d",
				ErrMirrorBackpressure,
				mirror.nextEmit,
			)
		}
	}
	return nil
}

func (mirror *Mirror) runSink() {
	defer close(mirror.done)
	discard := false
	for frame := range mirror.queue {
		var sinkErr error
		if !discard {
			mirror.mu.Lock()
			discard = mirror.failure != nil ||
				mirror.state == mirrorAborted ||
				mirror.sinkContext.Err() != nil
			mirror.mu.Unlock()
		}
		if !discard {
			sinkErr = mirror.sink.WriteFrame(mirror.sinkContext, frame.index, frame.payload)
		}

		mirror.mu.Lock()
		mirror.allocated--
		firstFailure := false
		if sinkErr != nil {
			discard = true
			if mirror.state != mirrorAborted {
				firstFailure = mirror.failLocked(
					fmt.Errorf("write provisional capture stream frame %d: %w", frame.index, sinkErr),
				)
			}
		} else if !discard && mirror.state != mirrorAborted {
			mirror.delivered++
		}
		mirror.mu.Unlock()
		if firstFailure {
			mirror.sinkCancel()
			mirror.cancelCapture()
		}
	}
}

func (mirror *Mirror) writableErrorLocked() error {
	if mirror.failure != nil {
		return mirror.failure
	}
	switch mirror.state {
	case mirrorOpen:
		return nil
	case mirrorSealing, mirrorSealed:
		return ErrMirrorSealed
	case mirrorAborted:
		return ErrMirrorAborted
	default:
		return errors.New("capture stream mirror has invalid state")
	}
}

func (mirror *Mirror) recordFailureLocked(err error) bool {
	if mirror.failure != nil {
		return false
	}
	mirror.failure = err
	return true
}

func (mirror *Mirror) failLocked(err error) bool {
	first := mirror.recordFailureLocked(err)
	mirror.abortLocked()
	return first
}

func (mirror *Mirror) abortedErrorLocked() error {
	if mirror.failure != nil {
		return mirror.failure
	}
	return ErrMirrorAborted
}

func (mirror *Mirror) abortLocked() {
	if mirror.state == mirrorSealed || mirror.state == mirrorAborted {
		return
	}
	mirror.state = mirrorAborted
	mirror.allocated -= len(mirror.slots)
	clear(mirror.slots)
	mirror.closeQueueLocked()
}

func (mirror *Mirror) closeQueueLocked() {
	if mirror.queueClosed {
		return
	}
	close(mirror.queue)
	mirror.queueClosed = true
}

func (mirror *Mirror) cancelCapture() {
	mirror.cancelOnce.Do(mirror.cancel)
}

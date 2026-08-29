package dca

import (
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	frameReorderBytes = int64(4 * MaximumDataPayloadSize)
)

// FrameWriter converts offset-addressed DCA payloads into ordered whole frames.
// Its fixed window tolerates bounded UDP reordering without allowing a missing
// frame to grow memory indefinitely.
type FrameWriter struct {
	output     io.Writer
	frameBytes int64
	limit      uint64
	next       uint64
	frames     map[uint64]*pendingFrame
	spare      *pendingFrame
	failed     error
}

type pendingFrame struct {
	data    []byte
	ranges  []byteRange
	covered int64
}

type frameSegment struct {
	index       uint64
	frameStart  int
	frameEnd    int
	payloadFrom int
}

func NewFrameWriter(frameBytes int64, output io.Writer) (*FrameWriter, error) {
	if frameBytes <= 0 || frameBytes > int64(maxInt()) {
		return nil, fmt.Errorf("stream frame size must be in 1..%d, got %d", maxInt(), frameBytes)
	}
	if output == nil {
		return nil, errors.New("stream frame output is required")
	}
	limit := uint64(frameReorderBytes-1)/uint64(frameBytes) + 2
	return &FrameWriter{
		output: output, frameBytes: frameBytes, limit: limit,
		frames: make(map[uint64]*pendingFrame),
	}, nil
}

func (writer *FrameWriter) WriteAt(payload []byte, offset int64) (int, error) {
	if writer == nil {
		return 0, errors.New("nil stream frame writer")
	}
	if writer.failed != nil {
		return 0, writer.failed
	}
	if offset < 0 || int64(len(payload)) > math.MaxInt64-offset {
		return writer.fail("stream frame write range is invalid")
	}
	if len(payload) == 0 {
		return 0, nil
	}

	var segmentStorage [2]frameSegment
	segments := segmentStorage[:0]
	remaining := len(payload)
	payloadFrom := 0
	position := offset
	for remaining > 0 {
		index := uint64(position / writer.frameBytes)
		start := int(position % writer.frameBytes)
		part := min(remaining, int(writer.frameBytes)-start)
		segments = append(segments, frameSegment{
			index: index, frameStart: start, frameEnd: start + part, payloadFrom: payloadFrom,
		})
		remaining -= part
		payloadFrom += part
		position += int64(part)
	}
	for _, segment := range segments {
		if segment.index < writer.next {
			return writer.fail(fmt.Sprintf("stream data overlaps emitted frame %d", segment.index))
		}
		if segment.index-writer.next >= writer.limit {
			return writer.fail(fmt.Sprintf("stream data has an unresolved gap before frame %d", segment.index))
		}
		if frame := writer.frames[segment.index]; frame != nil &&
			frame.has(segment.frameStart, segment.frameEnd) {
			return writer.fail(fmt.Sprintf("stream data overlaps frame %d", segment.index))
		}
	}

	for _, segment := range segments {
		frame := writer.frames[segment.index]
		if frame == nil {
			frame = writer.takeFrame()
			writer.frames[segment.index] = frame
		}
		length := segment.frameEnd - segment.frameStart
		copy(frame.data[segment.frameStart:segment.frameEnd], payload[segment.payloadFrom:segment.payloadFrom+length])
		frame.mark(segment.frameStart, segment.frameEnd)
	}
	if err := writer.flush(); err != nil {
		writer.failed = err
		return 0, err
	}
	return len(payload), nil
}

func (writer *FrameWriter) flush() error {
	for {
		frame := writer.frames[writer.next]
		if frame == nil || frame.covered != writer.frameBytes {
			return nil
		}
		if err := writeAll(writer.output, frame.data); err != nil {
			return fmt.Errorf("write complete ADC frame %d: %w", writer.next, err)
		}
		delete(writer.frames, writer.next)
		frame.ranges = frame.ranges[:0]
		frame.covered = 0
		writer.spare = frame
		writer.next++
	}
}

func (writer *FrameWriter) takeFrame() *pendingFrame {
	if writer.spare == nil {
		return newPendingFrame(int(writer.frameBytes))
	}
	frame := writer.spare
	writer.spare = nil
	return frame
}

func (writer *FrameWriter) fail(message string) (int, error) {
	writer.failed = errors.New(message)
	return 0, writer.failed
}

func newPendingFrame(size int) *pendingFrame {
	return &pendingFrame{data: make([]byte, size)}
}

func (frame *pendingFrame) has(start, end int) bool {
	for _, current := range frame.ranges {
		if current.end <= int64(start) {
			continue
		}
		if current.start >= int64(end) {
			break
		}
		if current.end > int64(start) && current.start < int64(end) {
			return true
		}
	}
	return false
}

func (frame *pendingFrame) mark(start, end int) {
	frame.ranges = addRange(frame.ranges, byteRange{start: int64(start), end: int64(end)})
	frame.covered += int64(end - start)
}

func writeAll(output io.Writer, payload []byte) error {
	for len(payload) != 0 {
		written, err := output.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

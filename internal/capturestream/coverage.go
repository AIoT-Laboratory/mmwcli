package capturestream

import (
	"errors"
	"fmt"
)

const maxMirrorRangesPerFrame = 256

var errMirrorOverlap = errors.New("capture stream mirror range overlaps existing coverage")

type coverageRange struct {
	start int64
	end   int64
}

type frameSpool struct {
	index   uint64
	data    []byte
	ranges  []coverageRange
	covered int64
}

func newFrameSpool(index uint64, frameBytes int64) *frameSpool {
	return &frameSpool{
		index: index,
		data:  make([]byte, int(frameBytes)),
	}
}

func (frame *frameSpool) preflight(start, end int64) error {
	if frame == nil || start < 0 || end <= start || end > int64(len(frame.data)) {
		return fmt.Errorf(
			"%w: invalid frame %d range [%d,%d)",
			ErrMirrorIntegrity,
			frameIndex(frame),
			start,
			end,
		)
	}
	resultingCount := len(frame.ranges) + 1
	for _, current := range frame.ranges {
		if current.end < start {
			continue
		}
		if current.start > end {
			break
		}
		if current.end > start && current.start < end {
			return fmt.Errorf(
				"%w: %w in frame %d at [%d,%d)",
				ErrMirrorIntegrity,
				errMirrorOverlap,
				frame.index,
				start,
				end,
			)
		}
		// Exact adjacency merges without concealing a gap.
		resultingCount--
	}
	if resultingCount > maxMirrorRangesPerFrame {
		return fmt.Errorf(
			"%w: frame %d exceeds %d sparse coverage ranges",
			ErrMirrorBackpressure,
			frame.index,
			maxMirrorRangesPerFrame,
		)
	}
	return nil
}

func (frame *frameSpool) apply(start int64, payload []byte) {
	end := start + int64(len(payload))
	copy(frame.data[int(start):int(end)], payload)
	frame.ranges = addCoverageRange(frame.ranges, coverageRange{start: start, end: end})
	frame.covered += int64(len(payload))
}

func (frame *frameSpool) complete() bool {
	return frame != nil &&
		frame.covered == int64(len(frame.data)) &&
		len(frame.ranges) == 1 &&
		frame.ranges[0].start == 0 &&
		frame.ranges[0].end == int64(len(frame.data))
}

func addCoverageRange(ranges []coverageRange, addition coverageRange) []coverageRange {
	index := 0
	for index < len(ranges) && ranges[index].end < addition.start {
		index++
	}
	for index < len(ranges) && ranges[index].start <= addition.end {
		if ranges[index].start < addition.start {
			addition.start = ranges[index].start
		}
		if ranges[index].end > addition.end {
			addition.end = ranges[index].end
		}
		ranges = append(ranges[:index], ranges[index+1:]...)
	}
	ranges = append(ranges, coverageRange{})
	copy(ranges[index+1:], ranges[index:])
	ranges[index] = addition
	return ranges
}

func frameIndex(frame *frameSpool) uint64 {
	if frame == nil {
		return 0
	}
	return frame.index
}

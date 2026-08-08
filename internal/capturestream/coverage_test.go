package capturestream

import (
	"errors"
	"testing"
)

func TestFrameSpoolCompletesAcrossOutOfOrderAdjacentRanges(t *testing.T) {
	frame := newFrameSpool(7, 8)
	fragments := []struct {
		start   int64
		payload []byte
	}{
		{start: 4, payload: []byte{4, 5}},
		{start: 0, payload: []byte{0, 1}},
		{start: 6, payload: []byte{6, 7}},
		{start: 2, payload: []byte{2, 3}},
	}
	for _, fragment := range fragments {
		end := fragment.start + int64(len(fragment.payload))
		if err := frame.preflight(fragment.start, end); err != nil {
			t.Fatalf("preflight [%d,%d): %v", fragment.start, end, err)
		}
		frame.apply(fragment.start, fragment.payload)
	}
	if !frame.complete() {
		t.Fatalf("frame did not complete: ranges=%v covered=%d", frame.ranges, frame.covered)
	}
	want := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	for index := range want {
		if frame.data[index] != want[index] {
			t.Fatalf("frame byte %d = %d, want %d", index, frame.data[index], want[index])
		}
	}
}

func TestFrameSpoolRejectsOverlapWithoutMutation(t *testing.T) {
	frame := newFrameSpool(2, 8)
	if err := frame.preflight(1, 4); err != nil {
		t.Fatal(err)
	}
	frame.apply(1, []byte{1, 2, 3})

	if err := frame.preflight(3, 5); !errors.Is(err, errMirrorOverlap) {
		t.Fatalf("overlap error = %v, want %v", err, errMirrorOverlap)
	}
	if frame.covered != 3 || len(frame.ranges) != 1 || frame.ranges[0] != (coverageRange{start: 1, end: 4}) {
		t.Fatalf("overlap mutated coverage: ranges=%v covered=%d", frame.ranges, frame.covered)
	}
}

func TestFrameSpoolBoundsSparseCoverage(t *testing.T) {
	frame := newFrameSpool(0, 2*(maxMirrorRangesPerFrame+1))
	for index := range maxMirrorRangesPerFrame {
		start := int64(index * 2)
		if err := frame.preflight(start, start+1); err != nil {
			t.Fatalf("range %d preflight: %v", index, err)
		}
		frame.apply(start, []byte{1})
	}
	start := int64(maxMirrorRangesPerFrame * 2)
	if err := frame.preflight(start, start+1); !errors.Is(err, ErrMirrorBackpressure) {
		t.Fatalf("sparse range error = %v, want %v", err, ErrMirrorBackpressure)
	}
	if len(frame.ranges) != maxMirrorRangesPerFrame {
		t.Fatalf("range count = %d, want %d", len(frame.ranges), maxMirrorRangesPerFrame)
	}
}

func TestFrameSpoolRejectsInvalidBounds(t *testing.T) {
	frame := newFrameSpool(3, 8)
	for _, bounds := range [][2]int64{{-1, 1}, {0, 0}, {4, 3}, {0, 9}} {
		if err := frame.preflight(bounds[0], bounds[1]); !errors.Is(err, ErrMirrorIntegrity) {
			t.Fatalf("preflight(%d, %d) error = %v", bounds[0], bounds[1], err)
		}
	}
}

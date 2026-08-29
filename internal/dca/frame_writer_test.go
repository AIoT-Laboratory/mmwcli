package dca

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestFrameWriterPublishesOnlyOrderedCompleteFrames(t *testing.T) {
	var output bytes.Buffer
	writer, err := NewFrameWriter(4, &output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("efgh"), 4); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("later frame was published before frame zero: %q", output.Bytes())
	}
	if _, err := writer.WriteAt([]byte("abcd"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("ijkl"), 8); err != nil {
		t.Fatal(err)
	}
	if output.String() != "abcdefghijkl" {
		t.Fatalf("stream = %q", output.String())
	}
}

func TestFrameWriterSplitsPacketsAcrossFrameBoundaries(t *testing.T) {
	var output bytes.Buffer
	writer, err := NewFrameWriter(4, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []struct {
		payload string
		offset  int64
	}{
		{payload: "cdef", offset: 2},
		{payload: "ab", offset: 0},
		{payload: "gh", offset: 6},
	} {
		if _, err := writer.WriteAt([]byte(write.payload), write.offset); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != "abcdefgh" {
		t.Fatalf("stream = %q", output.String())
	}
}

func TestFrameWriterRejectsOverlapAndUnboundedGap(t *testing.T) {
	for _, test := range []struct {
		name  string
		write func(*FrameWriter) error
		match string
	}{
		{
			name: "overlap",
			write: func(writer *FrameWriter) error {
				if _, err := writer.WriteAt([]byte("ab"), 0); err != nil {
					return err
				}
				_, err := writer.WriteAt([]byte("z"), 1)
				return err
			},
			match: "overlaps",
		},
		{
			name: "gap",
			write: func(writer *FrameWriter) error {
				_, err := writer.WriteAt([]byte("x"), int64(writer.limit)*writer.frameBytes)
				return err
			},
			match: "unresolved gap",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer, err := NewFrameWriter(4, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.write(writer); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestFrameWriterDoesNotReserveFourLargeFrames(t *testing.T) {
	writer, err := NewFrameWriter(frameReorderBytes*2, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if writer.limit != 2 {
		t.Fatalf("large-frame reorder limit = %d, want 2", writer.limit)
	}
}

func TestFrameWriterPropagatesDownstreamFailure(t *testing.T) {
	want := errors.New("pipe closed")
	writer, err := NewFrameWriter(4, errorWriter{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("abcd"), 0); !errors.Is(err, want) {
		t.Fatalf("WriteAt error = %v, want %v", err, want)
	}
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

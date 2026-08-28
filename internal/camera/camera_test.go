package camera

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReadJPEGDoesNotSplitStuffedEntropyBytes(t *testing.T) {
	want := []byte{
		0xff, 0xd8,
		0xff, 0xe0, 0x00, 0x02,
		0xff, 0xda, 0x00, 0x02,
		0x01, 0xff, 0x00, 0x02,
		0xff, 0xd9,
	}
	got, err := readJPEG(bufio.NewReader(bytes.NewReader(want)), len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("JPEG differs: %x", got)
	}
}

func TestCameraIndexIsMinimalAndContiguous(t *testing.T) {
	encoded, err := encodeIndex([]entry{
		{offset: 0, size: 10, receivedNS: 100},
		{offset: 10, size: 12, receivedNS: 140},
	}, 22)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded[:8]) != indexMagic || len(encoded) != indexHeaderBytes+2*indexEntryBytes {
		t.Fatalf("unexpected camera index: %x", encoded)
	}
	if got := binary.LittleEndian.Uint64(encoded[16:24]); got != 2 {
		t.Fatalf("item count = %d", got)
	}
	if _, err := encodeIndex([]entry{{offset: 1, size: 10}}, 10); err == nil {
		t.Fatal("non-contiguous index accepted")
	}
}

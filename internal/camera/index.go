package camera

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	indexMagic       = "MMWCAM01"
	indexHeaderBytes = 24
	indexEntryBytes  = 24
)

type entry struct {
	offset     uint64
	size       uint64
	receivedNS uint64
}

func encodeIndex(entries []entry, payloadBytes uint64) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("camera produced no complete JPEG frames")
	}
	if len(entries) > (math.MaxInt-indexHeaderBytes)/indexEntryBytes {
		return nil, errors.New("camera index is too large")
	}
	encoded := make([]byte, indexHeaderBytes+len(entries)*indexEntryBytes)
	copy(encoded[:8], indexMagic)
	binary.LittleEndian.PutUint32(encoded[8:12], 1)
	binary.LittleEndian.PutUint32(encoded[12:16], indexHeaderBytes)
	binary.LittleEndian.PutUint64(encoded[16:24], uint64(len(entries)))
	wantOffset := uint64(0)
	lastTime := uint64(0)
	for index, item := range entries {
		if item.offset != wantOffset || item.size == 0 || (index != 0 && item.receivedNS < lastTime) {
			return nil, errors.New("camera index is not contiguous and time ordered")
		}
		start := indexHeaderBytes + index*indexEntryBytes
		binary.LittleEndian.PutUint64(encoded[start:start+8], item.offset)
		binary.LittleEndian.PutUint64(encoded[start+8:start+16], item.size)
		binary.LittleEndian.PutUint64(encoded[start+16:start+24], item.receivedNS)
		wantOffset += item.size
		lastTime = item.receivedNS
	}
	if wantOffset != payloadBytes {
		return nil, errors.New("camera index does not cover its payload")
	}
	return encoded, nil
}

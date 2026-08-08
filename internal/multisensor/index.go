package multisensor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
)

const (
	SensorIndexSchema      = "mmwcli.sensor_index.v1"
	SensorIndexMagic       = "MMWSIDX1"
	SensorIndexMajor       = uint16(1)
	SensorIndexHeaderBytes = uint16(32)
	SensorIndexEntryBytes  = uint16(64)
	NoSyncEventID          = uint64(math.MaxUint64)

	MaximumIndexItems   = uint64(1 << 24)
	MaximumItemBytes    = uint64(1 << 34)
	MaximumPayloadBytes = uint64(1 << 50)
)

// SensorIndex is the decoded mmwcli.sensor_index.v1 header and entry sequence.
// ItemCount is always len(Entries); PayloadBytes is copied from the header.
type SensorIndex struct {
	PayloadBytes uint64
	Entries      []IndexEntry
}

// IndexEntry is the exact 64-byte v1 entry. Flags and Reserved are exposed so
// validation never silently discards future or corrupt values.
type IndexEntry struct {
	ItemIndex     uint64
	PayloadOffset uint64
	PayloadSize   uint64
	Tick          uint64
	WrapCount     uint64
	DurationTicks uint64
	SyncEventID   uint64
	Flags         uint32
	Reserved      uint32
}

// EncodeSensorIndex validates and encodes one complete fixed-layout index.
func EncodeSensorIndex(index SensorIndex, limits SourceLimits) ([]byte, error) {
	if err := index.Validate(limits); err != nil {
		return nil, err
	}
	size, err := encodedIndexSize(uint64(len(index.Entries)))
	if err != nil {
		return nil, err
	}
	if size > uint64(maxInt()) {
		return nil, errors.New("sensor index is too large for this platform")
	}
	encoded := make([]byte, int(size))
	copy(encoded[:8], SensorIndexMagic)
	binary.LittleEndian.PutUint16(encoded[8:10], SensorIndexMajor)
	binary.LittleEndian.PutUint16(encoded[10:12], SensorIndexHeaderBytes)
	binary.LittleEndian.PutUint16(encoded[12:14], SensorIndexEntryBytes)
	// Header flags at 14:16 are zero in v1.
	binary.LittleEndian.PutUint64(encoded[16:24], uint64(len(index.Entries)))
	binary.LittleEndian.PutUint64(encoded[24:32], index.PayloadBytes)
	for item, entry := range index.Entries {
		offset := int(SensorIndexHeaderBytes) + item*int(SensorIndexEntryBytes)
		putIndexEntry(encoded[offset:offset+int(SensorIndexEntryBytes)], entry)
	}
	return encoded, nil
}

// DecodeSensorIndex rejects truncated, trailing, future-version, over-limit,
// noncontiguous, or otherwise noncanonical v1 indices.
func DecodeSensorIndex(encoded []byte, limits SourceLimits) (SensorIndex, error) {
	if err := limits.Validate(); err != nil {
		return SensorIndex{}, fmt.Errorf("sensor index limits: %w", err)
	}
	if len(encoded) < int(SensorIndexHeaderBytes) {
		return SensorIndex{}, fmt.Errorf(
			"sensor index is truncated: got %d bytes, need at least %d",
			len(encoded),
			SensorIndexHeaderBytes,
		)
	}
	if string(encoded[:8]) != SensorIndexMagic {
		return SensorIndex{}, fmt.Errorf("sensor index magic is %q, want %q", encoded[:8], SensorIndexMagic)
	}
	if major := binary.LittleEndian.Uint16(encoded[8:10]); major != SensorIndexMajor {
		return SensorIndex{}, fmt.Errorf("sensor index major version is %d, want %d", major, SensorIndexMajor)
	}
	if headerBytes := binary.LittleEndian.Uint16(encoded[10:12]); headerBytes != SensorIndexHeaderBytes {
		return SensorIndex{}, fmt.Errorf("sensor index header size is %d, want %d", headerBytes, SensorIndexHeaderBytes)
	}
	if entryBytes := binary.LittleEndian.Uint16(encoded[12:14]); entryBytes != SensorIndexEntryBytes {
		return SensorIndex{}, fmt.Errorf("sensor index entry size is %d, want %d", entryBytes, SensorIndexEntryBytes)
	}
	if flags := binary.LittleEndian.Uint16(encoded[14:16]); flags != 0 {
		return SensorIndex{}, fmt.Errorf("sensor index header flags are %#x, want zero", flags)
	}
	itemCount := binary.LittleEndian.Uint64(encoded[16:24])
	payloadBytes := binary.LittleEndian.Uint64(encoded[24:32])
	if itemCount > limits.MaxItems {
		return SensorIndex{}, fmt.Errorf("sensor index item count %d exceeds declared limit %d", itemCount, limits.MaxItems)
	}
	if payloadBytes > limits.MaxPayloadBytes {
		return SensorIndex{}, fmt.Errorf(
			"sensor index payload bytes %d exceed declared limit %d",
			payloadBytes,
			limits.MaxPayloadBytes,
		)
	}
	expectedBytes, err := encodedIndexSize(itemCount)
	if err != nil {
		return SensorIndex{}, err
	}
	if uint64(len(encoded)) != expectedBytes {
		return SensorIndex{}, fmt.Errorf(
			"sensor index length is %d, header requires exactly %d",
			len(encoded),
			expectedBytes,
		)
	}
	if itemCount > uint64(maxInt()) {
		return SensorIndex{}, errors.New("sensor index item count is too large for this platform")
	}
	entries := make([]IndexEntry, int(itemCount))
	for item := range entries {
		offset := int(SensorIndexHeaderBytes) + item*int(SensorIndexEntryBytes)
		entries[item] = readIndexEntry(encoded[offset : offset+int(SensorIndexEntryBytes)])
	}
	index := SensorIndex{PayloadBytes: payloadBytes, Entries: entries}
	if err := index.Validate(limits); err != nil {
		return SensorIndex{}, err
	}
	return index, nil
}

// Validate enforces the fixed v1 ordering and exact payload coverage contract.
func (index SensorIndex) Validate(limits SourceLimits) error {
	if err := limits.Validate(); err != nil {
		return fmt.Errorf("sensor index limits: %w", err)
	}
	if uint64(len(index.Entries)) > limits.MaxItems {
		return fmt.Errorf(
			"sensor index has %d items, declared maximum is %d",
			len(index.Entries),
			limits.MaxItems,
		)
	}
	if index.PayloadBytes > limits.MaxPayloadBytes {
		return fmt.Errorf(
			"sensor index covers %d payload bytes, declared maximum is %d",
			index.PayloadBytes,
			limits.MaxPayloadBytes,
		)
	}
	nextOffset := uint64(0)
	for item, entry := range index.Entries {
		if entry.ItemIndex != uint64(item) {
			return fmt.Errorf("sensor index entry %d has item index %d", item, entry.ItemIndex)
		}
		if entry.PayloadOffset != nextOffset {
			return fmt.Errorf(
				"sensor index entry %d starts at payload byte %d, want contiguous offset %d",
				item,
				entry.PayloadOffset,
				nextOffset,
			)
		}
		if entry.PayloadSize == 0 {
			return fmt.Errorf("sensor index entry %d has an empty payload", item)
		}
		if entry.PayloadSize > limits.MaxItemBytes {
			return fmt.Errorf(
				"sensor index entry %d payload size %d exceeds declared maximum %d",
				item,
				entry.PayloadSize,
				limits.MaxItemBytes,
			)
		}
		end, ok := checkedAddU64(entry.PayloadOffset, entry.PayloadSize)
		if !ok {
			return fmt.Errorf("sensor index entry %d payload range overflows uint64", item)
		}
		if end > index.PayloadBytes {
			return fmt.Errorf(
				"sensor index entry %d ends at byte %d beyond declared payload size %d",
				item,
				end,
				index.PayloadBytes,
			)
		}
		if entry.Flags != 0 || entry.Reserved != 0 {
			return fmt.Errorf(
				"sensor index entry %d has flags=%#x reserved=%#x; v1 requires zero",
				item,
				entry.Flags,
				entry.Reserved,
			)
		}
		nextOffset = end
	}
	if nextOffset != index.PayloadBytes {
		return fmt.Errorf(
			"sensor index covers %d payload bytes, header declares %d",
			nextOffset,
			index.PayloadBytes,
		)
	}
	return nil
}

func encodedIndexSize(itemCount uint64) (uint64, error) {
	entriesBytes, ok := checkedMulU64(itemCount, uint64(SensorIndexEntryBytes))
	if !ok {
		return 0, errors.New("sensor index encoded length overflows uint64")
	}
	size, ok := checkedAddU64(uint64(SensorIndexHeaderBytes), entriesBytes)
	if !ok {
		return 0, errors.New("sensor index encoded length overflows uint64")
	}
	return size, nil
}

func putIndexEntry(encoded []byte, entry IndexEntry) {
	binary.LittleEndian.PutUint64(encoded[0:8], entry.ItemIndex)
	binary.LittleEndian.PutUint64(encoded[8:16], entry.PayloadOffset)
	binary.LittleEndian.PutUint64(encoded[16:24], entry.PayloadSize)
	binary.LittleEndian.PutUint64(encoded[24:32], entry.Tick)
	binary.LittleEndian.PutUint64(encoded[32:40], entry.WrapCount)
	binary.LittleEndian.PutUint64(encoded[40:48], entry.DurationTicks)
	binary.LittleEndian.PutUint64(encoded[48:56], entry.SyncEventID)
	binary.LittleEndian.PutUint32(encoded[56:60], entry.Flags)
	binary.LittleEndian.PutUint32(encoded[60:64], entry.Reserved)
}

func readIndexEntry(encoded []byte) IndexEntry {
	return IndexEntry{
		ItemIndex:     binary.LittleEndian.Uint64(encoded[0:8]),
		PayloadOffset: binary.LittleEndian.Uint64(encoded[8:16]),
		PayloadSize:   binary.LittleEndian.Uint64(encoded[16:24]),
		Tick:          binary.LittleEndian.Uint64(encoded[24:32]),
		WrapCount:     binary.LittleEndian.Uint64(encoded[32:40]),
		DurationTicks: binary.LittleEndian.Uint64(encoded[40:48]),
		SyncEventID:   binary.LittleEndian.Uint64(encoded[48:56]),
		Flags:         binary.LittleEndian.Uint32(encoded[56:60]),
		Reserved:      binary.LittleEndian.Uint32(encoded[60:64]),
	}
}

func checkedAddU64(left, right uint64) (uint64, bool) {
	sum, carry := bits.Add64(left, right, 0)
	return sum, carry == 0
}

func checkedMulU64(left, right uint64) (uint64, bool) {
	high, low := bits.Mul64(left, right)
	return low, high == 0
}

func maxInt() int { return int(^uint(0) >> 1) }

package multisensor

import (
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSensorIndexCodecUsesExactFixedLayout(t *testing.T) {
	limits := SourceLimits{MaxItems: 8, MaxItemBytes: 16, MaxPayloadBytes: 64}
	index := SensorIndex{
		PayloadBytes: 12,
		Entries: []IndexEntry{
			{
				ItemIndex: 0, PayloadOffset: 0, PayloadSize: 5,
				Tick: 7, WrapCount: 2, DurationTicks: 3, SyncEventID: NoSyncEventID,
			},
			{
				ItemIndex: 1, PayloadOffset: 5, PayloadSize: 7,
				Tick: 11, WrapCount: 2, DurationTicks: 4, SyncEventID: 9,
			},
		},
	}
	encoded, err := EncodeSensorIndex(index, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 32+2*64 || string(encoded[:8]) != SensorIndexMagic ||
		binary.LittleEndian.Uint16(encoded[8:10]) != 1 ||
		binary.LittleEndian.Uint16(encoded[10:12]) != 32 ||
		binary.LittleEndian.Uint16(encoded[12:14]) != 64 ||
		binary.LittleEndian.Uint16(encoded[14:16]) != 0 ||
		binary.LittleEndian.Uint64(encoded[16:24]) != 2 ||
		binary.LittleEndian.Uint64(encoded[24:32]) != 12 {
		t.Fatalf("unexpected sensor-index header: %x", encoded[:32])
	}
	second := encoded[32+64 : 32+2*64]
	if binary.LittleEndian.Uint64(second[0:8]) != 1 ||
		binary.LittleEndian.Uint64(second[8:16]) != 5 ||
		binary.LittleEndian.Uint64(second[16:24]) != 7 ||
		binary.LittleEndian.Uint64(second[24:32]) != 11 ||
		binary.LittleEndian.Uint64(second[32:40]) != 2 ||
		binary.LittleEndian.Uint64(second[40:48]) != 4 ||
		binary.LittleEndian.Uint64(second[48:56]) != 9 ||
		binary.LittleEndian.Uint32(second[56:60]) != 0 ||
		binary.LittleEndian.Uint32(second[60:64]) != 0 {
		t.Fatalf("unexpected second sensor-index entry: %x", second)
	}
	decoded, err := DecodeSensorIndex(encoded, limits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, index) {
		t.Fatalf("decoded index = %+v, want %+v", decoded, index)
	}
}

func TestSensorIndexRejectsNoncanonicalOrUnboundedInput(t *testing.T) {
	limits := SourceLimits{MaxItems: 4, MaxItemBytes: 8, MaxPayloadBytes: 16}
	valid := SensorIndex{
		PayloadBytes: 8,
		Entries: []IndexEntry{{
			ItemIndex: 0, PayloadSize: 8, SyncEventID: NoSyncEventID,
		}},
	}
	encoded, err := EncodeSensorIndex(valid, limits)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		match  string
	}{
		{name: "magic", mutate: func(value []byte) []byte { value[0] = 'X'; return value }, match: "magic"},
		{name: "major", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[8:10], 2); return value }, match: "major"},
		{name: "header bytes", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[10:12], 31); return value }, match: "header size"},
		{name: "entry bytes", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[12:14], 63); return value }, match: "entry size"},
		{name: "header flags", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint16(value[14:16], 1); return value }, match: "header flags"},
		{name: "item bound", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint64(value[16:24], 5); return value }, match: "item count"},
		{name: "payload bound", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint64(value[24:32], 17); return value }, match: "payload bytes"},
		{name: "trailing", mutate: func(value []byte) []byte { return append(value, 0) }, match: "length"},
		{name: "item index", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint64(value[32:40], 1); return value }, match: "item index"},
		{name: "payload offset", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint64(value[40:48], 1); return value }, match: "contiguous"},
		{name: "empty item", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint64(value[48:56], 0); return value }, match: "empty payload"},
		{name: "entry flags", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint32(value[88:92], 1); return value }, match: "requires zero"},
		{name: "reserved", mutate: func(value []byte) []byte { binary.LittleEndian.PutUint32(value[92:96], 1); return value }, match: "requires zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := append([]byte(nil), encoded...)
			corrupt = test.mutate(corrupt)
			if _, err := DecodeSensorIndex(corrupt, limits); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("DecodeSensorIndex error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestSensorIndexCoverageAndClockWrapAreChecked(t *testing.T) {
	limits := SourceLimits{MaxItems: 4, MaxItemBytes: 8, MaxPayloadBytes: 16}
	for _, test := range []struct {
		name  string
		index SensorIndex
		match string
	}{
		{
			name: "gap",
			index: SensorIndex{PayloadBytes: 8, Entries: []IndexEntry{
				{ItemIndex: 0, PayloadSize: 4},
				{ItemIndex: 1, PayloadOffset: 5, PayloadSize: 3},
			}},
			match: "contiguous",
		},
		{
			name: "does not cover payload",
			index: SensorIndex{PayloadBytes: 9, Entries: []IndexEntry{
				{ItemIndex: 0, PayloadSize: 8},
			}},
			match: "covers 8",
		},
		{
			name: "item over limit",
			index: SensorIndex{PayloadBytes: 9, Entries: []IndexEntry{
				{ItemIndex: 0, PayloadSize: 9},
			}},
			match: "exceeds declared maximum",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.index.Validate(limits); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate error = %v, want %q", err, test.match)
			}
		})
	}

	wrapping := Clock{ClockID: "counter", TickHz: 1, WrapTicks: 10, TimestampSemantics: TimestampFrameStart}
	if got, err := UnwrapTicks(wrapping, 3, 2); err != nil || got != 23 {
		t.Fatalf("UnwrapTicks = %d, %v, want 23", got, err)
	}
	if _, err := UnwrapTicks(wrapping, 10, 0); err == nil || !strings.Contains(err.Error(), "below") {
		t.Fatalf("out-of-range tick error = %v", err)
	}
	if _, err := UnwrapTicks(wrapping, 1, math.MaxUint64); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("wrap multiplication error = %v", err)
	}
	nonWrapping := Clock{ClockID: "counter", TickHz: 1, TimestampSemantics: TimestampFrameStart}
	if _, err := UnwrapTicks(nonWrapping, 1, 1); err == nil || !strings.Contains(err.Error(), "must be zero") {
		t.Fatalf("non-wrapping clock error = %v", err)
	}
}

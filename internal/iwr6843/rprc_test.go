package iwr6843

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func TestParseRPRCPlansStudioCompatibleMemoryWrites(t *testing.T) {
	imageData := makeRPRCFixture(
		0x12345678,
		fixtureSection{address: 0x1000, data: make([]byte, 4096)},
		fixtureSection{address: 0x3000, data: make([]byte, 4100)},
		fixtureSection{address: 0x08000000, data: []byte{1, 2, 3, 4}},
	)
	image, err := parseRPRC(imageData)
	if err != nil {
		t.Fatal(err)
	}
	if image.entryPoints[0] != 0x12345678 || image.version != 1 || len(image.sections) != 3 {
		t.Fatalf("unexpected image metadata: %+v", image)
	}
	if image.sections[1].declaredSize != 4100 || len(image.sections[1].data) != 4104 {
		t.Fatalf("second section sizes = %d/%d, want 4100/4104", image.sections[1].declaredSize, len(image.sections[1].data))
	}

	bssWrites, err := planMemoryWrites(image, rprcTargetBSS)
	if err != nil {
		t.Fatal(err)
	}
	if len(bssWrites) != 4 {
		t.Fatalf("BSS write count = %d, want 4", len(bssWrites))
	}
	assertWrite(t, bssWrites[0], 0, 0x40001000, 4096)
	assertWrite(t, bssWrites[1], 1, 0x40003000, 4096)
	assertWrite(t, bssWrites[2], 1, 0x40004000, 8)
	assertWrite(t, bssWrites[3], 2, 0x41000000, 8)

	mssImage, err := parseRPRC(makeRPRCFixture(
		0x12345678,
		fixtureSection{address: 0x1000, data: make([]byte, 4096)},
		fixtureSection{address: mssHighAddressBase, data: make([]byte, 4100)},
	))
	if err != nil {
		t.Fatal(err)
	}
	mssWrites, err := planMemoryWrites(mssImage, rprcTargetMSS)
	if err != nil {
		t.Fatal(err)
	}
	assertWrite(t, mssWrites[0], 0, 0x00001000, 4096)
	assertWrite(t, mssWrites[1], 1, 0x08030000, 4096)
	assertWrite(t, mssWrites[2], 1, 0x08031000, 8)
}

func TestParseRPRCRejectsMalformedImages(t *testing.T) {
	valid := makeRPRCFixture(0, fixtureSection{address: 0x1000, data: []byte("data")})

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "short header", data: valid[:23], want: "header is truncated"},
		{name: "bad magic", data: mutateRPRC(valid, func(data []byte) { binary.LittleEndian.PutUint32(data[0:4], 0) }), want: "invalid RPRC magic"},
		{name: "truncated table", data: mutateRPRC(valid, func(data []byte) { binary.LittleEndian.PutUint32(data[12:16], 2) }), want: "section table is truncated"},
		{name: "unaligned address", data: mutateRPRC(valid, func(data []byte) { binary.LittleEndian.PutUint64(data[24:32], 1) }), want: "address is not 4-byte aligned"},
		{name: "unaligned size", data: mutateRPRC(valid, func(data []byte) { binary.LittleEndian.PutUint32(data[32:36], 2) }), want: "size is not 4-byte aligned"},
		{name: "truncated payload", data: valid[:len(valid)-1], want: "payload is truncated"},
		{name: "trailing data", data: append(append([]byte(nil), valid...), 0), want: "trailing data"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRPRC(test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseRPRC() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPlanMemoryWritesRejectsInvalidTargetAndAddressRange(t *testing.T) {
	valid, err := parseRPRC(makeRPRCFixture(0, fixtureSection{address: math.MaxUint32 - 3, data: []byte("data")}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planMemoryWrites(valid, rprcTargetMSS); err == nil || !strings.Contains(err.Error(), "exceeds 32-bit") {
		t.Fatalf("range error = %v", err)
	}
	if _, err := planMemoryWrites(valid, rprcTarget(255)); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("target error = %v", err)
	}
}

func TestMapSectionAddressEnforcesXWR68xxMemoryWindows(t *testing.T) {
	valid := []struct {
		name    string
		target  rprcTarget
		address uint64
		size    uint64
		want    uint64
	}{
		{name: "MSS low window", target: rprcTargetMSS, address: 0, size: mssLowAddressEnd, want: 0},
		{name: "MSS high window", target: rprcTargetMSS, address: mssHighAddressBase, size: mssHighAddressEnd - mssHighAddressBase, want: mssHighAddressBase},
		{name: "BSS low window", target: rprcTargetBSS, address: 0, size: bssLowAddressEnd, want: bssLowAddressAdd},
		{name: "BSS high window", target: rprcTargetBSS, address: bssHighAddressBase, size: bssHighAddressEnd - bssHighAddressBase, want: bssHighAddressBase + bssHighAddressAdd},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			got, err := mapSectionAddress(test.address, test.size, test.target)
			if err != nil || got != test.want {
				t.Fatalf("mapSectionAddress() = (0x%X, %v), want (0x%X, nil)", got, err, test.want)
			}
		})
	}

	invalid := []struct {
		name    string
		target  rprcTarget
		address uint64
	}{
		{name: "MSS above low window", target: rprcTargetMSS, address: mssLowAddressEnd},
		{name: "MSS below high window", target: rprcTargetMSS, address: bssHighAddressBase},
		{name: "BSS above low window", target: rprcTargetBSS, address: bssLowAddressEnd},
		{name: "BSS above high window", target: rprcTargetBSS, address: bssHighAddressEnd},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := mapSectionAddress(test.address, 4, test.target); err == nil || !strings.Contains(err.Error(), "outside xWR68xx") {
				t.Fatalf("mapSectionAddress() error = %v, want memory-window error", err)
			}
		})
	}
}

func assertWrite(t *testing.T, write memoryWrite, section int, address uint32, size int) {
	t.Helper()
	if write.section != section || write.address != address || len(write.data) != size {
		t.Fatalf("write = section %d address 0x%08X size %d, want section %d address 0x%08X size %d", write.section, write.address, len(write.data), section, address, size)
	}
}

type fixtureSection struct {
	address uint64
	data    []byte
}

func makeRPRCFixture(entryPoint uint32, sections ...fixtureSection) []byte {
	image := make([]byte, 24)
	binary.LittleEndian.PutUint32(image[0:4], rprcMagic)
	binary.LittleEndian.PutUint32(image[4:8], entryPoint)
	binary.LittleEndian.PutUint32(image[12:16], uint32(len(sections)))
	binary.LittleEndian.PutUint32(image[16:20], 1)
	for _, section := range sections {
		if len(section.data)%4 != 0 {
			panic("RPRC fixture section data must be 4-byte aligned")
		}
		header := make([]byte, 24)
		binary.LittleEndian.PutUint64(header[0:8], section.address)
		binary.LittleEndian.PutUint32(header[8:12], uint32(len(section.data)))
		image = append(image, header...)
		image = append(image, section.data...)
		image = append(image, make([]byte, len(section.data)%8)...)
	}
	return image
}

func mutateRPRC(source []byte, mutate func([]byte)) []byte {
	result := append([]byte(nil), source...)
	mutate(result)
	return result
}

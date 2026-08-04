package debugcapture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	rprcMagic             = uint32(0x43525052)
	rprcHeaderSize        = uint64(24)
	rprcSectionHeaderSize = uint64(24)
	maxMemoryWriteSize    = uint64(4096)

	mssLowAddressEnd   = uint64(0x00180000)
	mssHighAddressBase = uint64(0x08030000)
	mssHighAddressEnd  = uint64(0x080B0000)
	bssLowAddressEnd   = uint64(0x00200000)
	bssHighAddressBase = uint64(0x08000000)
	bssHighAddressEnd  = uint64(0x08010000)
	bssLowAddressAdd   = uint64(0x40000000)
	bssHighAddressAdd  = uint64(0x39000000)
)

type rprcTarget uint8

const (
	rprcTargetUnknown rprcTarget = iota
	rprcTargetMSS
	rprcTargetBSS
)

type rprcImage struct {
	entryPoints [2]uint32
	version     uint32
	sections    []rprcSection
}

type rprcSection struct {
	address      uint64
	declaredSize uint32
	data         []byte
}

type memoryWrite struct {
	section int
	address uint32
	data    []byte
}

func parseRPRC(data []byte) (rprcImage, error) {
	if uint64(len(data)) < rprcHeaderSize {
		return rprcImage{}, fmt.Errorf("RPRC header is truncated: size=%d", len(data))
	}
	if magic := binary.LittleEndian.Uint32(data[0:4]); magic != rprcMagic {
		return rprcImage{}, fmt.Errorf("invalid RPRC magic: expected=0x%08X actual=0x%08X", rprcMagic, magic)
	}

	sectionCount := binary.LittleEndian.Uint32(data[12:16])
	maximumSectionCount := (uint64(len(data)) - rprcHeaderSize) / rprcSectionHeaderSize
	if uint64(sectionCount) > maximumSectionCount {
		return rprcImage{}, fmt.Errorf(
			"RPRC section table is truncated: count=%d maximum=%d",
			sectionCount,
			maximumSectionCount,
		)
	}

	image := rprcImage{
		entryPoints: [2]uint32{
			binary.LittleEndian.Uint32(data[4:8]),
			binary.LittleEndian.Uint32(data[8:12]),
		},
		version:  binary.LittleEndian.Uint32(data[16:20]),
		sections: make([]rprcSection, 0, int(sectionCount)),
	}
	cursor := rprcHeaderSize
	for sectionIndex := range int(sectionCount) {
		headerEnd := cursor + rprcSectionHeaderSize
		if headerEnd > uint64(len(data)) {
			return rprcImage{}, fmt.Errorf("RPRC section %d header is truncated at offset %d", sectionIndex, cursor)
		}
		header := data[int(cursor):int(headerEnd)]
		address := binary.LittleEndian.Uint64(header[0:8])
		declaredSize := binary.LittleEndian.Uint32(header[8:12])
		if address%4 != 0 {
			return rprcImage{}, fmt.Errorf("RPRC section %d address is not 4-byte aligned: 0x%X", sectionIndex, address)
		}
		if declaredSize%4 != 0 {
			return rprcImage{}, fmt.Errorf("RPRC section %d size is not 4-byte aligned: %d", sectionIndex, declaredSize)
		}

		storedSize := uint64(declaredSize) + uint64(declaredSize%8)
		payloadEnd := headerEnd + storedSize
		if payloadEnd > uint64(len(data)) {
			return rprcImage{}, fmt.Errorf(
				"RPRC section %d payload is truncated: need=%d available=%d",
				sectionIndex,
				storedSize,
				uint64(len(data))-headerEnd,
			)
		}
		image.sections = append(image.sections, rprcSection{
			address:      address,
			declaredSize: declaredSize,
			data:         data[int(headerEnd):int(payloadEnd)],
		})
		cursor = payloadEnd
	}
	if cursor != uint64(len(data)) {
		return rprcImage{}, fmt.Errorf("RPRC has trailing data: offset=%d size=%d", cursor, len(data))
	}
	return image, nil
}

func planMemoryWrites(image rprcImage, target rprcTarget) ([]memoryWrite, error) {
	if target != rprcTargetMSS && target != rprcTargetBSS {
		return nil, errors.New("unknown RPRC write target")
	}

	writes := make([]memoryWrite, 0)
	for sectionIndex, section := range image.sections {
		address, err := mapSectionAddress(section.address, uint64(len(section.data)), target)
		if err != nil {
			return nil, fmt.Errorf("RPRC section %d: %w", sectionIndex, err)
		}
		for offset := uint64(0); offset < uint64(len(section.data)); offset += maxMemoryWriteSize {
			end := min(offset+maxMemoryWriteSize, uint64(len(section.data)))
			writes = append(writes, memoryWrite{
				section: sectionIndex,
				address: uint32(address + offset),
				data:    section.data[int(offset):int(end)],
			})
		}
	}
	return writes, nil
}

func mapSectionAddress(address, size uint64, target rprcTarget) (uint64, error) {
	if address > math.MaxUint32 || size > uint64(math.MaxUint32)+1-address {
		return 0, fmt.Errorf("write range exceeds 32-bit address space: address=0x%X size=%d", address, size)
	}
	end := address + size

	var mapped uint64
	switch target {
	case rprcTargetMSS:
		if (address >= mssHighAddressBase && end <= mssHighAddressEnd) ||
			(address < bssHighAddressBase && end <= mssLowAddressEnd) {
			mapped = address
		} else {
			return 0, fmt.Errorf("write range is outside xWR68xx MSS memory: address=0x%X size=%d", address, size)
		}
	case rprcTargetBSS:
		if address >= bssHighAddressBase && end <= bssHighAddressEnd {
			mapped = address + bssHighAddressAdd
		} else if end <= bssLowAddressEnd {
			mapped = address + bssLowAddressAdd
		} else {
			return 0, fmt.Errorf("write range is outside xWR68xx BSS memory: address=0x%X size=%d", address, size)
		}
	default:
		return 0, errors.New("unknown RPRC write target")
	}
	if mapped > math.MaxUint32 || size > uint64(math.MaxUint32)+1-mapped {
		return 0, fmt.Errorf("mapped write range exceeds 32-bit address space: address=0x%X size=%d", mapped, size)
	}
	return mapped, nil
}

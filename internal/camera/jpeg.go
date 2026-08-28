package camera

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// readJPEG reads exactly one image without decoding pixels. Marker lengths
// and entropy byte stuffing are followed so payload bytes cannot split a
// frame early.
func readJPEG(reader *bufio.Reader, maximum int) ([]byte, error) {
	if maximum < 4 {
		return nil, errors.New("maximum JPEG size must be at least 4 bytes")
	}
	frame := bytes.NewBuffer(make([]byte, 0, min(maximum, 64<<10)))
	readByte := func() (byte, error) {
		if frame.Len() == maximum {
			return 0, fmt.Errorf("JPEG exceeds %d bytes", maximum)
		}
		value, err := reader.ReadByte()
		if err == nil {
			_ = frame.WriteByte(value)
		}
		return value, err
	}
	readBytes := func(count int) error {
		if count < 0 || count > maximum-frame.Len() {
			return fmt.Errorf("JPEG exceeds %d bytes", maximum)
		}
		start := frame.Len()
		frame.Grow(count)
		frame.Write(make([]byte, count))
		_, err := io.ReadFull(reader, frame.Bytes()[start:start+count])
		return err
	}

	first, err := readByte()
	if err != nil {
		return frame.Bytes(), err
	}
	second, err := readByte()
	if err != nil {
		return frame.Bytes(), io.ErrUnexpectedEOF
	}
	if first != 0xff || second != 0xd8 {
		return frame.Bytes(), errors.New("camera output must begin with JPEG SOI ff d8")
	}

	var pending byte
	for {
		marker := pending
		pending = 0
		if marker == 0 {
			prefix, markerErr := readByte()
			if markerErr != nil {
				return frame.Bytes(), io.ErrUnexpectedEOF
			}
			if prefix != 0xff {
				return frame.Bytes(), errors.New("JPEG marker prefix missing outside scan data")
			}
			for {
				marker, markerErr = readByte()
				if markerErr != nil {
					return frame.Bytes(), io.ErrUnexpectedEOF
				}
				if marker != 0xff {
					break
				}
			}
		}

		switch {
		case marker == 0xd9:
			return append([]byte(nil), frame.Bytes()...), nil
		case marker == 0xd8 || marker == 0x00:
			return frame.Bytes(), fmt.Errorf("invalid JPEG marker ff %02x", marker)
		case marker == 0x01 || marker >= 0xd0 && marker <= 0xd7:
			continue
		}

		high, lengthErr := readByte()
		if lengthErr != nil {
			return frame.Bytes(), io.ErrUnexpectedEOF
		}
		low, lengthErr := readByte()
		if lengthErr != nil {
			return frame.Bytes(), io.ErrUnexpectedEOF
		}
		length := int(high)<<8 | int(low)
		if length < 2 {
			return frame.Bytes(), fmt.Errorf("JPEG marker ff %02x has invalid length %d", marker, length)
		}
		if err := readBytes(length - 2); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return frame.Bytes(), io.ErrUnexpectedEOF
			}
			return frame.Bytes(), err
		}
		if marker != 0xda {
			continue
		}

		for {
			value, scanErr := readByte()
			if scanErr != nil {
				return frame.Bytes(), io.ErrUnexpectedEOF
			}
			if value != 0xff {
				continue
			}
			for {
				marker, scanErr = readByte()
				if scanErr != nil {
					return frame.Bytes(), io.ErrUnexpectedEOF
				}
				if marker != 0xff {
					break
				}
			}
			if marker == 0x00 || marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			if marker == 0xd9 {
				return append([]byte(nil), frame.Bytes()...), nil
			}
			pending = marker
			break
		}
	}
}

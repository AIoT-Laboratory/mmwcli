package fixedframeproducer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type frameEvent struct {
	payload   []byte
	consumed  chan struct{}
	sequence  uint64
	readBytes int
	readErr   error
}

type cameraChild struct {
	command *exec.Cmd
	output  *os.File

	stopOnce sync.Once
	stop     chan struct{}
	waitDone chan struct{}
	waitErr  error

	events   chan frameEvent
	readDone chan struct{}
	complete atomic.Uint64
}

func startCameraChild(
	ctx context.Context,
	argv []string,
	stderr io.Writer,
) (*cameraChild, error) {
	output, childOutput, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdout = childOutput
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = output.Close()
		_ = childOutput.Close()
		return nil, err
	}
	if err := childOutput.Close(); err != nil {
		_ = output.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	child := &cameraChild{
		command:  command,
		output:   output,
		stop:     make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	go func() {
		child.waitErr = command.Wait()
		close(child.waitDone)
	}()
	return child, nil
}

func (child *cameraChild) streamFixed(frameBytes int) <-chan frameEvent {
	child.events = make(chan frameEvent)
	child.readDone = make(chan struct{})
	go child.readFrames(frameBytes)
	return child.events
}

func (child *cameraChild) streamJPEG(maxFrameBytes int) <-chan frameEvent {
	child.events = make(chan frameEvent)
	child.readDone = make(chan struct{})
	go child.readJPEGFrames(maxFrameBytes)
	return child.events
}

func (child *cameraChild) readFrames(frameBytes int) {
	defer close(child.events)
	defer close(child.readDone)
	buffer := make([]byte, frameBytes)
	for {
		readBytes, err := io.ReadFull(child.output, buffer)
		if err != nil {
			select {
			case child.events <- frameEvent{readBytes: readBytes, readErr: err}:
			case <-child.stop:
			}
			return
		}
		consumed := make(chan struct{})
		sequence := child.complete.Add(1)
		select {
		case child.events <- frameEvent{
			payload: buffer, consumed: consumed, sequence: sequence,
		}:
		case <-child.stop:
			return
		}
		select {
		case <-consumed:
		case <-child.stop:
			return
		}
	}
}

func (child *cameraChild) readJPEGFrames(maxFrameBytes int) {
	defer close(child.events)
	defer close(child.readDone)
	reader := bufio.NewReaderSize(child.output, 64<<10)
	for {
		payload, err := readJPEGFrame(reader, maxFrameBytes)
		if err != nil {
			select {
			case child.events <- frameEvent{readBytes: len(payload), readErr: err}:
			case <-child.stop:
			}
			return
		}
		consumed := make(chan struct{})
		sequence := child.complete.Add(1)
		select {
		case child.events <- frameEvent{
			payload: payload, consumed: consumed, sequence: sequence,
		}:
		case <-child.stop:
			return
		}
		select {
		case <-consumed:
		case <-child.stop:
			return
		}
	}
}

func (child *cameraChild) completedFrames() uint64 {
	return child.complete.Load()
}

// readJPEGFrame reads exactly one JPEG image without decoding its pixels. It
// follows marker lengths and entropy byte stuffing so embedded marker-looking
// bytes cannot split an item early.
func readJPEGFrame(reader *bufio.Reader, maxFrameBytes int) ([]byte, error) {
	if maxFrameBytes < 4 {
		return nil, errors.New("JPEG maximum item size must be at least 4 bytes")
	}
	frame := bytes.NewBuffer(make([]byte, 0, min(maxFrameBytes, 64<<10)))
	readByte := func() (byte, error) {
		if frame.Len() == maxFrameBytes {
			return 0, fmt.Errorf("JPEG frame exceeds %d bytes", maxFrameBytes)
		}
		value, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		_ = frame.WriteByte(value)
		return value, nil
	}
	readBytes := func(count int) error {
		if count < 0 || count > maxFrameBytes-frame.Len() {
			return fmt.Errorf("JPEG frame exceeds %d bytes", maxFrameBytes)
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
		return frame.Bytes(), errors.New("JPEG child output must begin with SOI marker ff d8")
	}

	var pendingMarker byte
	for {
		marker := pendingMarker
		pendingMarker = 0
		if marker == 0 {
			prefix, markerErr := readByte()
			if markerErr != nil {
				return frame.Bytes(), io.ErrUnexpectedEOF
			}
			if prefix != 0xff {
				return frame.Bytes(), errors.New("JPEG expected a marker prefix outside scan data")
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
			return frame.Bytes(), fmt.Errorf("JPEG contains invalid marker ff %02x", marker)
		case marker == 0x01 || marker >= 0xd0 && marker <= 0xd7:
			continue
		}

		lengthHigh, lengthErr := readByte()
		if lengthErr != nil {
			return frame.Bytes(), io.ErrUnexpectedEOF
		}
		lengthLow, lengthErr := readByte()
		if lengthErr != nil {
			return frame.Bytes(), io.ErrUnexpectedEOF
		}
		length := int(lengthHigh)<<8 | int(lengthLow)
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
			pendingMarker = marker
			break
		}
	}
}

func (child *cameraChild) terminate() (bool, error) {
	alreadyExited := false
	select {
	case <-child.waitDone:
		alreadyExited = true
	default:
	}
	child.stopOnce.Do(func() {
		close(child.stop)
		_ = child.output.Close()
		if !alreadyExited && child.command.Process != nil {
			_ = child.command.Process.Kill()
		}
	})
	<-child.waitDone
	if child.readDone != nil {
		<-child.readDone
	}
	return alreadyExited, child.waitErr
}

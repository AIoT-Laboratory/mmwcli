package d2xx

import (
	"fmt"
	"io"
	"sync"
)

const (
	BitModeReset        = byte(0x00)
	BitModeAsyncBitBang = byte(0x01)
	BitModeMPSSE        = byte(0x02)
)

type nativeDevice interface {
	close() Status
	read([]byte) (uint32, Status)
	write([]byte) (uint32, Status)
	queueStatus() (uint32, Status)
	setTimeouts(uint32, uint32) Status
	setChars(byte, byte, byte, byte) Status
	setLatencyTimer(byte) Status
	setBitMode(byte, byte) Status
	getBitMode() (byte, Status)
	setBaudRate(uint32) Status
	setUSBParameters(uint32, uint32) Status
}

type Device struct {
	mu      sync.Mutex
	native  nativeDevice
	release func()
}

func (device *Device) Close() error {
	if device == nil {
		return nil
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.native == nil {
		return nil
	}
	status := device.native.close()
	device.native = nil
	if device.release != nil {
		device.release()
	}
	device.release = nil
	return statusError("FT_Close", status)
}

func (device *Device) Read(buffer []byte) (int, error) {
	if device == nil {
		return 0, ErrDeviceClosed
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.native == nil {
		return 0, ErrDeviceClosed
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if uint64(len(buffer)) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("D2XX read is too large: %d", len(buffer))
	}
	count, status := device.native.read(buffer)
	if uint64(count) > uint64(len(buffer)) {
		return 0, fmt.Errorf("D2XX FT_Read returned invalid byte count %d for %d-byte buffer", count, len(buffer))
	}
	return int(count), statusError("FT_Read", status)
}

func (device *Device) Write(buffer []byte) (int, error) {
	if device == nil {
		return 0, ErrDeviceClosed
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.native == nil {
		return 0, ErrDeviceClosed
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if uint64(len(buffer)) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("D2XX write is too large: %d", len(buffer))
	}
	count, status := device.native.write(buffer)
	if uint64(count) > uint64(len(buffer)) {
		return 0, fmt.Errorf("D2XX FT_Write returned invalid byte count %d for %d-byte buffer", count, len(buffer))
	}
	if err := statusError("FT_Write", status); err != nil {
		return int(count), err
	}
	if int(count) != len(buffer) {
		return int(count), io.ErrShortWrite
	}
	return int(count), nil
}

func (device *Device) QueueStatus() (uint32, error) {
	if device == nil {
		return 0, ErrDeviceClosed
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.native == nil {
		return 0, ErrDeviceClosed
	}
	count, status := device.native.queueStatus()
	return count, statusError("FT_GetQueueStatus", status)
}

func (device *Device) SetTimeouts(readMilliseconds, writeMilliseconds uint32) error {
	return device.apply("FT_SetTimeouts", func(native nativeDevice) Status {
		return native.setTimeouts(readMilliseconds, writeMilliseconds)
	})
}

func (device *Device) SetChars(eventChar byte, eventEnabled bool, errorChar byte, errorEnabled bool) error {
	return device.apply("FT_SetChars", func(native nativeDevice) Status {
		return native.setChars(eventChar, boolByte(eventEnabled), errorChar, boolByte(errorEnabled))
	})
}

func (device *Device) SetLatencyTimer(milliseconds byte) error {
	return device.apply("FT_SetLatencyTimer", func(native nativeDevice) Status {
		return native.setLatencyTimer(milliseconds)
	})
}

func (device *Device) SetBitMode(mask, mode byte) error {
	return device.apply("FT_SetBitMode", func(native nativeDevice) Status {
		return native.setBitMode(mask, mode)
	})
}

func (device *Device) GetBitMode() (byte, error) {
	if device == nil {
		return 0, ErrDeviceClosed
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.native == nil {
		return 0, ErrDeviceClosed
	}
	mode, status := device.native.getBitMode()
	return mode, statusError("FT_GetBitMode", status)
}

func (device *Device) SetBaudRate(baud uint32) error {
	return device.apply("FT_SetBaudRate", func(native nativeDevice) Status {
		return native.setBaudRate(baud)
	})
}

func (device *Device) SetUSBParameters(inputSize, outputSize uint32) error {
	return device.apply("FT_SetUSBParameters", func(native nativeDevice) Status {
		return native.setUSBParameters(inputSize, outputSize)
	})
}

func (device *Device) apply(operation string, call func(nativeDevice) Status) error {
	if device == nil {
		return ErrDeviceClosed
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.native == nil {
		return ErrDeviceClosed
	}
	return statusError(operation, call(device.native))
}

func statusError(operation string, status Status) error {
	if status == StatusOK {
		return nil
	}
	return &StatusError{Operation: operation, Status: status}
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

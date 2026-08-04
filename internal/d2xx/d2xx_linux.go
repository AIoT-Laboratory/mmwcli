//go:build linux && cgo && ftd2xx

package d2xx

/*
#cgo LDFLAGS: -lftd2xx
#include <stdlib.h>
#include <ftd2xx.h>
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

type linuxLibrary struct{}
type linuxDevice struct{ handle C.FT_HANDLE }

func openNative() (nativeLibrary, error) {
	return &linuxLibrary{}, nil
}

func (*linuxLibrary) info() (nativeInfo, error) {
	return nativeInfo{library: "libftd2xx.so"}, nil
}

func (*linuxLibrary) open(selector Selector) (nativeDevice, error) {
	argument := C.CString(selector.Value)
	if argument == nil {
		return nil, errors.New("allocate D2XX selector")
	}
	defer C.free(unsafe.Pointer(argument))
	var handle C.FT_HANDLE
	status := Status(C.FT_OpenEx(unsafe.Pointer(argument), C.DWORD(selector.By), &handle))
	if err := statusError("FT_OpenEx", status); err != nil {
		return nil, err
	}
	if handle == nil {
		return nil, errors.New("D2XX FT_OpenEx returned a nil handle")
	}
	return &linuxDevice{handle: handle}, nil
}

func (*linuxLibrary) close() error { return nil }

func (device *linuxDevice) close() Status {
	return Status(C.FT_Close(device.handle))
}

func (device *linuxDevice) read(buffer []byte) (uint32, Status) {
	var count C.DWORD
	status := Status(C.FT_Read(device.handle, unsafe.Pointer(&buffer[0]), C.DWORD(len(buffer)), &count))
	runtime.KeepAlive(buffer)
	return uint32(count), status
}

func (device *linuxDevice) write(buffer []byte) (uint32, Status) {
	var count C.DWORD
	status := Status(C.FT_Write(device.handle, unsafe.Pointer(&buffer[0]), C.DWORD(len(buffer)), &count))
	runtime.KeepAlive(buffer)
	return uint32(count), status
}

func (device *linuxDevice) queueStatus() (uint32, Status) {
	var count C.DWORD
	status := Status(C.FT_GetQueueStatus(device.handle, &count))
	return uint32(count), status
}

func (device *linuxDevice) setTimeouts(readMilliseconds, writeMilliseconds uint32) Status {
	return Status(C.FT_SetTimeouts(device.handle, C.ULONG(readMilliseconds), C.ULONG(writeMilliseconds)))
}

func (device *linuxDevice) setChars(eventChar, eventEnabled, errorChar, errorEnabled byte) Status {
	return Status(C.FT_SetChars(
		device.handle,
		C.UCHAR(eventChar),
		C.UCHAR(eventEnabled),
		C.UCHAR(errorChar),
		C.UCHAR(errorEnabled),
	))
}

func (device *linuxDevice) setLatencyTimer(milliseconds byte) Status {
	return Status(C.FT_SetLatencyTimer(device.handle, C.UCHAR(milliseconds)))
}

func (device *linuxDevice) setBitMode(mask, mode byte) Status {
	return Status(C.FT_SetBitMode(device.handle, C.UCHAR(mask), C.UCHAR(mode)))
}

func (device *linuxDevice) setUSBParameters(inputSize, outputSize uint32) Status {
	return Status(C.FT_SetUSBParameters(device.handle, C.ULONG(inputSize), C.ULONG(outputSize)))
}

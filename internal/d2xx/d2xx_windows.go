//go:build windows && ftd2xx

package d2xx

import (
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var procGetSystemDirectoryW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")

type windowsLibrary struct {
	path    string
	library *syscall.DLL
	openEx  *syscall.Proc
	device  windowsDeviceProcedures
}

type windowsDeviceProcedures struct {
	close            *syscall.Proc
	read             *syscall.Proc
	write            *syscall.Proc
	queueStatus      *syscall.Proc
	setTimeouts      *syscall.Proc
	setChars         *syscall.Proc
	setLatencyTimer  *syscall.Proc
	setBitMode       *syscall.Proc
	getBitMode       *syscall.Proc
	setBaudRate      *syscall.Proc
	setUSBParameters *syscall.Proc
}

type windowsDevice struct {
	handle     uintptr
	procedures windowsDeviceProcedures
}

func openNative() (nativeLibrary, error) {
	systemDirectory, err := windowsSystemDirectory()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(systemDirectory, "ftd2xx.dll")
	library, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("load FTDI D2XX library %s: %w", path, err)
	}
	native := &windowsLibrary{path: path, library: library}
	bindings := []struct {
		name   string
		target **syscall.Proc
	}{
		{name: "FT_OpenEx", target: &native.openEx},
		{name: "FT_Close", target: &native.device.close},
		{name: "FT_Read", target: &native.device.read},
		{name: "FT_Write", target: &native.device.write},
		{name: "FT_GetQueueStatus", target: &native.device.queueStatus},
		{name: "FT_SetTimeouts", target: &native.device.setTimeouts},
		{name: "FT_SetChars", target: &native.device.setChars},
		{name: "FT_SetLatencyTimer", target: &native.device.setLatencyTimer},
		{name: "FT_SetBitMode", target: &native.device.setBitMode},
		{name: "FT_GetBitMode", target: &native.device.getBitMode},
		{name: "FT_SetBaudRate", target: &native.device.setBaudRate},
		{name: "FT_SetUSBParameters", target: &native.device.setUSBParameters},
	}
	for _, binding := range bindings {
		procedure, findErr := library.FindProc(binding.name)
		if findErr == nil {
			*binding.target = procedure
			continue
		}
		_ = library.Release()
		return nil, fmt.Errorf("resolve %s in %s: %w", binding.name, path, findErr)
	}
	return native, nil
}

func (library *windowsLibrary) open(selector Selector) (nativeDevice, error) {
	argument, err := syscall.BytePtrFromString(selector.Description)
	if err != nil {
		return nil, fmt.Errorf("encode D2XX selector: %w", err)
	}
	var handle uintptr
	status := callD2XX(
		library.openEx,
		uintptr(unsafe.Pointer(argument)),
		uintptr(2), // FT_OPEN_BY_DESCRIPTION
		uintptr(unsafe.Pointer(&handle)),
	)
	runtime.KeepAlive(argument)
	if err := statusError("FT_OpenEx", status); err != nil {
		return nil, err
	}
	if handle == 0 {
		return nil, fmt.Errorf("D2XX FT_OpenEx returned a nil handle")
	}
	return &windowsDevice{handle: handle, procedures: library.device}, nil
}

func (library *windowsLibrary) close() error {
	if err := library.library.Release(); err != nil {
		return fmt.Errorf("release FTDI D2XX library %s: %w", library.path, err)
	}
	return nil
}

func (device *windowsDevice) close() Status {
	return callD2XX(device.procedures.close, device.handle)
}

func (device *windowsDevice) read(buffer []byte) (uint32, Status) {
	var count uint32
	status := callD2XX(
		device.procedures.read,
		device.handle,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&count)),
	)
	runtime.KeepAlive(buffer)
	return count, status
}

func (device *windowsDevice) write(buffer []byte) (uint32, Status) {
	var count uint32
	status := callD2XX(
		device.procedures.write,
		device.handle,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&count)),
	)
	runtime.KeepAlive(buffer)
	return count, status
}

func (device *windowsDevice) queueStatus() (uint32, Status) {
	var count uint32
	status := callD2XX(device.procedures.queueStatus, device.handle, uintptr(unsafe.Pointer(&count)))
	return count, status
}

func (device *windowsDevice) setTimeouts(readMilliseconds, writeMilliseconds uint32) Status {
	return callD2XX(device.procedures.setTimeouts, device.handle, uintptr(readMilliseconds), uintptr(writeMilliseconds))
}

func (device *windowsDevice) setChars(eventChar, eventEnabled, errorChar, errorEnabled byte) Status {
	return callD2XX(
		device.procedures.setChars,
		device.handle,
		uintptr(eventChar),
		uintptr(eventEnabled),
		uintptr(errorChar),
		uintptr(errorEnabled),
	)
}

func (device *windowsDevice) setLatencyTimer(milliseconds byte) Status {
	return callD2XX(device.procedures.setLatencyTimer, device.handle, uintptr(milliseconds))
}

func (device *windowsDevice) setBitMode(mask, mode byte) Status {
	return callD2XX(device.procedures.setBitMode, device.handle, uintptr(mask), uintptr(mode))
}

func (device *windowsDevice) getBitMode() (byte, Status) {
	var mode byte
	status := callD2XX(device.procedures.getBitMode, device.handle, uintptr(unsafe.Pointer(&mode)))
	return mode, status
}

func (device *windowsDevice) setBaudRate(baud uint32) Status {
	return callD2XX(device.procedures.setBaudRate, device.handle, uintptr(baud))
}

func (device *windowsDevice) setUSBParameters(inputSize, outputSize uint32) Status {
	return callD2XX(device.procedures.setUSBParameters, device.handle, uintptr(inputSize), uintptr(outputSize))
}

func callD2XX(procedure *syscall.Proc, arguments ...uintptr) Status {
	result, _, _ := procedure.Call(arguments...)
	return Status(uint32(result))
}

func windowsSystemDirectory() (string, error) {
	buffer := make([]uint16, syscall.MAX_PATH)
	for {
		length, _, callError := procGetSystemDirectoryW.Call(
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
		)
		if length == 0 {
			if callError != nil && callError != syscall.Errno(0) {
				return "", fmt.Errorf("query Windows system directory: %w", callError)
			}
			return "", fmt.Errorf("query Windows system directory")
		}
		if length < uintptr(len(buffer)) {
			return syscall.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, int(length)+1)
	}
}

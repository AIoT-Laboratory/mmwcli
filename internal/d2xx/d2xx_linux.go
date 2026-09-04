//go:build linux && cgo

package d2xx

/*
#cgo LDFLAGS: -ldl

#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

typedef uint32_t FT_STATUS;
typedef void *FT_HANDLE;

typedef FT_STATUS (*ft_open_ex_fn)(void *, uint32_t, FT_HANDLE *);
typedef FT_STATUS (*ft_close_fn)(FT_HANDLE);
typedef FT_STATUS (*ft_read_fn)(FT_HANDLE, void *, uint32_t, uint32_t *);
typedef FT_STATUS (*ft_write_fn)(FT_HANDLE, void *, uint32_t, uint32_t *);
typedef FT_STATUS (*ft_queue_status_fn)(FT_HANDLE, uint32_t *);
typedef FT_STATUS (*ft_set_timeouts_fn)(FT_HANDLE, uint32_t, uint32_t);
typedef FT_STATUS (*ft_set_chars_fn)(FT_HANDLE, uint8_t, uint8_t, uint8_t, uint8_t);
typedef FT_STATUS (*ft_set_latency_timer_fn)(FT_HANDLE, uint8_t);
typedef FT_STATUS (*ft_set_bit_mode_fn)(FT_HANDLE, uint8_t, uint8_t);
typedef FT_STATUS (*ft_get_bit_mode_fn)(FT_HANDLE, uint8_t *);
typedef FT_STATUS (*ft_set_baud_rate_fn)(FT_HANDLE, uint32_t);
typedef FT_STATUS (*ft_set_usb_parameters_fn)(FT_HANDLE, uint32_t, uint32_t);

typedef struct mmw_d2xx {
	void *so;
	ft_open_ex_fn open_ex;
	ft_close_fn close;
	ft_read_fn read;
	ft_write_fn write;
	ft_queue_status_fn queue_status;
	ft_set_timeouts_fn set_timeouts;
	ft_set_chars_fn set_chars;
	ft_set_latency_timer_fn set_latency_timer;
	ft_set_bit_mode_fn set_bit_mode;
	ft_get_bit_mode_fn get_bit_mode;
	ft_set_baud_rate_fn set_baud_rate;
	ft_set_usb_parameters_fn set_usb_parameters;
} mmw_d2xx;

static void mmw_d2xx_error(char *buffer, size_t size, const char *operation, const char *detail) {
	if (detail == NULL) detail = "unknown error";
	snprintf(buffer, size, "%s: %s", operation, detail);
}

static mmw_d2xx *mmw_d2xx_load(char *error, size_t error_size) {
	static const char *paths[] = {"libftd2xx.so", "libftd2xx.so.1", NULL};
	void *so = NULL;
	for (int index = 0; paths[index] != NULL; ++index) {
		so = dlopen(paths[index], RTLD_NOW | RTLD_LOCAL);
		if (so != NULL) {
			break;
		}
	}
	if (so == NULL) {
		mmw_d2xx_error(error, error_size, "load libftd2xx.so", dlerror());
		return NULL;
	}

	mmw_d2xx *library = (mmw_d2xx *)calloc(1, sizeof(mmw_d2xx));
	if (library == NULL) {
		mmw_d2xx_error(error, error_size, "allocate D2XX bindings", "out of memory");
		dlclose(so);
		return NULL;
	}
	library->so = so;

#define MMW_BIND(field, symbol) do { \
	dlerror(); \
	*(void **)(&library->field) = dlsym(so, symbol); \
	const char *bind_error = dlerror(); \
	if (bind_error != NULL) { \
		mmw_d2xx_error(error, error_size, "resolve " symbol, bind_error); \
		dlclose(so); \
		free(library); \
		return NULL; \
	} \
} while (0)

	MMW_BIND(open_ex, "FT_OpenEx");
	MMW_BIND(close, "FT_Close");
	MMW_BIND(read, "FT_Read");
	MMW_BIND(write, "FT_Write");
	MMW_BIND(queue_status, "FT_GetQueueStatus");
	MMW_BIND(set_timeouts, "FT_SetTimeouts");
	MMW_BIND(set_chars, "FT_SetChars");
	MMW_BIND(set_latency_timer, "FT_SetLatencyTimer");
	MMW_BIND(set_bit_mode, "FT_SetBitMode");
	MMW_BIND(get_bit_mode, "FT_GetBitMode");
	MMW_BIND(set_baud_rate, "FT_SetBaudRate");
	MMW_BIND(set_usb_parameters, "FT_SetUSBParameters");
#undef MMW_BIND

	return library;
}

static void mmw_d2xx_unload(mmw_d2xx *library) {
	if (library == NULL) return;
	dlclose(library->so);
	free(library);
}

static FT_STATUS mmw_d2xx_open(mmw_d2xx *library, char *description, FT_HANDLE *handle) {
	return library->open_ex(description, 2, handle);
}
static FT_STATUS mmw_d2xx_close(mmw_d2xx *library, FT_HANDLE handle) { return library->close(handle); }
static FT_STATUS mmw_d2xx_read(mmw_d2xx *library, FT_HANDLE handle, void *data, uint32_t size, uint32_t *count) { return library->read(handle, data, size, count); }
static FT_STATUS mmw_d2xx_write(mmw_d2xx *library, FT_HANDLE handle, void *data, uint32_t size, uint32_t *count) { return library->write(handle, data, size, count); }
static FT_STATUS mmw_d2xx_queue_status(mmw_d2xx *library, FT_HANDLE handle, uint32_t *count) { return library->queue_status(handle, count); }
static FT_STATUS mmw_d2xx_set_timeouts(mmw_d2xx *library, FT_HANDLE handle, uint32_t read_ms, uint32_t write_ms) { return library->set_timeouts(handle, read_ms, write_ms); }
static FT_STATUS mmw_d2xx_set_chars(mmw_d2xx *library, FT_HANDLE handle, uint8_t event_char, uint8_t event_enabled, uint8_t error_char, uint8_t error_enabled) { return library->set_chars(handle, event_char, event_enabled, error_char, error_enabled); }
static FT_STATUS mmw_d2xx_set_latency_timer(mmw_d2xx *library, FT_HANDLE handle, uint8_t ms) { return library->set_latency_timer(handle, ms); }
static FT_STATUS mmw_d2xx_set_bit_mode(mmw_d2xx *library, FT_HANDLE handle, uint8_t mask, uint8_t mode) { return library->set_bit_mode(handle, mask, mode); }
static FT_STATUS mmw_d2xx_get_bit_mode(mmw_d2xx *library, FT_HANDLE handle, uint8_t *mode) { return library->get_bit_mode(handle, mode); }
static FT_STATUS mmw_d2xx_set_baud_rate(mmw_d2xx *library, FT_HANDLE handle, uint32_t baud) { return library->set_baud_rate(handle, baud); }
static FT_STATUS mmw_d2xx_set_usb_parameters(mmw_d2xx *library, FT_HANDLE handle, uint32_t input_size, uint32_t output_size) { return library->set_usb_parameters(handle, input_size, output_size); }
*/
import "C"

import (
	"errors"
	"unsafe"
)

const linuxD2XXErrorSize = 512

type linuxLibrary struct {
	native *C.mmw_d2xx
}

type linuxDevice struct {
	library *C.mmw_d2xx
	handle  C.FT_HANDLE
}

func openNative() (nativeLibrary, error) {
	errorBuffer := make([]byte, linuxD2XXErrorSize)
	library := C.mmw_d2xx_load((*C.char)(unsafe.Pointer(&errorBuffer[0])), C.size_t(len(errorBuffer)))
	if library == nil {
		return nil, errors.New(cStringBuffer(errorBuffer))
	}
	return &linuxLibrary{native: library}, nil
}

func (library *linuxLibrary) open(selector Selector) (nativeDevice, error) {
	description := C.CString(selector.Description)
	defer C.free(unsafe.Pointer(description))
	var handle C.FT_HANDLE
	status := Status(C.mmw_d2xx_open(library.native, description, &handle))
	if err := statusError("FT_OpenEx", status); err != nil {
		return nil, err
	}
	if handle == nil {
		return nil, errors.New("D2XX FT_OpenEx returned a nil handle")
	}
	return &linuxDevice{library: library.native, handle: handle}, nil
}

func (library *linuxLibrary) close() error {
	if library.native == nil {
		return nil
	}
	C.mmw_d2xx_unload(library.native)
	library.native = nil
	return nil
}

func (device *linuxDevice) close() Status {
	return Status(C.mmw_d2xx_close(device.library, device.handle))
}

func (device *linuxDevice) read(buffer []byte) (uint32, Status) {
	var count C.uint32_t
	status := C.mmw_d2xx_read(
		device.library,
		device.handle,
		unsafe.Pointer(&buffer[0]),
		C.uint32_t(len(buffer)),
		&count,
	)
	return uint32(count), Status(status)
}

func (device *linuxDevice) write(buffer []byte) (uint32, Status) {
	var count C.uint32_t
	status := C.mmw_d2xx_write(
		device.library,
		device.handle,
		unsafe.Pointer(&buffer[0]),
		C.uint32_t(len(buffer)),
		&count,
	)
	return uint32(count), Status(status)
}

func (device *linuxDevice) queueStatus() (uint32, Status) {
	var count C.uint32_t
	status := C.mmw_d2xx_queue_status(device.library, device.handle, &count)
	return uint32(count), Status(status)
}

func (device *linuxDevice) setTimeouts(readMilliseconds, writeMilliseconds uint32) Status {
	return Status(C.mmw_d2xx_set_timeouts(device.library, device.handle, C.uint32_t(readMilliseconds), C.uint32_t(writeMilliseconds)))
}

func (device *linuxDevice) setChars(eventChar, eventEnabled, errorChar, errorEnabled byte) Status {
	return Status(C.mmw_d2xx_set_chars(device.library, device.handle, C.uint8_t(eventChar), C.uint8_t(eventEnabled), C.uint8_t(errorChar), C.uint8_t(errorEnabled)))
}

func (device *linuxDevice) setLatencyTimer(milliseconds byte) Status {
	return Status(C.mmw_d2xx_set_latency_timer(device.library, device.handle, C.uint8_t(milliseconds)))
}

func (device *linuxDevice) setBitMode(mask, mode byte) Status {
	return Status(C.mmw_d2xx_set_bit_mode(device.library, device.handle, C.uint8_t(mask), C.uint8_t(mode)))
}

func (device *linuxDevice) getBitMode() (byte, Status) {
	var mode C.uint8_t
	status := C.mmw_d2xx_get_bit_mode(device.library, device.handle, &mode)
	return byte(mode), Status(status)
}

func (device *linuxDevice) setBaudRate(baud uint32) Status {
	return Status(C.mmw_d2xx_set_baud_rate(device.library, device.handle, C.uint32_t(baud)))
}

func (device *linuxDevice) setUSBParameters(inputSize, outputSize uint32) Status {
	return Status(C.mmw_d2xx_set_usb_parameters(device.library, device.handle, C.uint32_t(inputSize), C.uint32_t(outputSize)))
}

func cStringBuffer(buffer []byte) string {
	for index, value := range buffer {
		if value == 0 {
			buffer = buffer[:index]
			break
		}
	}
	if len(buffer) == 0 {
		return "load FTDI D2XX library on Linux"
	}
	return string(buffer)
}

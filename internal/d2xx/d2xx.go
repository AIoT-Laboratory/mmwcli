// Package d2xx provides the narrow native FTDI D2XX boundary used by
// debug-capture builds. Loading the library does not enumerate or open devices.
package d2xx

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrLibraryClosed = errors.New("D2XX library is closed")
	ErrDeviceClosed  = errors.New("D2XX device is closed")
	ErrDevicesOpen   = errors.New("D2XX library still has open devices")
)

type Status uint32

const (
	StatusOK Status = iota
	StatusInvalidHandle
	StatusDeviceNotFound
	StatusDeviceNotOpened
	StatusIOError
	StatusInsufficientResources
	StatusInvalidParameter
	StatusInvalidBaudRate
	StatusDeviceNotOpenedForErase
	StatusDeviceNotOpenedForWrite
	StatusFailedToWriteDevice
	StatusEEPROMReadFailed
	StatusEEPROMWriteFailed
	StatusEEPROMEraseFailed
	StatusEEPROMNotPresent
	StatusEEPROMNotProgrammed
	StatusInvalidArguments
	StatusNotSupported
	StatusOtherError
	StatusDeviceListNotReady
)

var statusNames = [...]string{
	"OK",
	"invalid handle",
	"device not found",
	"device not opened",
	"I/O error",
	"insufficient resources",
	"invalid parameter",
	"invalid baud rate",
	"device not opened for erase",
	"device not opened for write",
	"failed to write device",
	"EEPROM read failed",
	"EEPROM write failed",
	"EEPROM erase failed",
	"EEPROM not present",
	"EEPROM not programmed",
	"invalid arguments",
	"not supported",
	"other error",
	"device list not ready",
}

func (status Status) String() string {
	if uint32(status) < uint32(len(statusNames)) {
		return statusNames[status]
	}
	return fmt.Sprintf("unknown status %d", status)
}

type StatusError struct {
	Operation string
	Status    Status
}

func (err *StatusError) Error() string {
	return fmt.Sprintf("D2XX %s: %s", err.Operation, err.Status)
}

type Version uint32

func (version Version) String() string {
	raw := uint32(version)
	if raw>>24 != 0 {
		return fmt.Sprintf("0x%08X", raw)
	}
	major, majorOK := decodeBCD(byte(raw >> 16))
	minor, minorOK := decodeBCD(byte(raw >> 8))
	build, buildOK := decodeBCD(byte(raw))
	if !majorOK || !minorOK || !buildOK {
		return fmt.Sprintf("0x%08X", raw)
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, build)
}

func decodeBCD(value byte) (int, bool) {
	high := value >> 4
	low := value & 0x0F
	if high > 9 || low > 9 {
		return 0, false
	}
	return int(high)*10 + int(low), true
}

type Info struct {
	Library      string
	Version      Version
	VersionKnown bool
}

type Selection uint32

const (
	SelectBySerialNumber Selection = 1
	SelectByDescription  Selection = 2
)

type Selector struct {
	By    Selection
	Value string
}

func (selector Selector) validate() error {
	if selector.By != SelectBySerialNumber && selector.By != SelectByDescription {
		return fmt.Errorf("invalid D2XX selection method %d", selector.By)
	}
	if selector.Value == "" {
		return errors.New("D2XX selector value is empty")
	}
	if strings.ContainsRune(selector.Value, 0) {
		return errors.New("D2XX selector contains NUL")
	}
	return nil
}

type nativeInfo struct {
	library      string
	version      Version
	versionKnown bool
}

type nativeLibrary interface {
	info() (nativeInfo, error)
	open(Selector) (nativeDevice, error)
	close() error
}

type Library struct {
	mu     sync.Mutex
	native nativeLibrary
	info   Info
	open   int
}

func Load() (*Library, error) {
	native, err := openNative()
	if err != nil {
		return nil, err
	}
	return finishLoad(native)
}

func finishLoad(native nativeLibrary) (*Library, error) {
	info, err := native.info()
	if err != nil {
		return nil, errors.Join(err, native.close())
	}
	return &Library{
		native: native,
		info: Info{
			Library:      info.library,
			Version:      info.version,
			VersionKnown: info.versionKnown,
		},
	}, nil
}

func (library *Library) Info() Info {
	if library == nil {
		return Info{}
	}
	return library.info
}

func (library *Library) Open(selector Selector) (*Device, error) {
	if err := selector.validate(); err != nil {
		return nil, err
	}
	if library == nil {
		return nil, ErrLibraryClosed
	}
	library.mu.Lock()
	defer library.mu.Unlock()
	if library.native == nil {
		return nil, ErrLibraryClosed
	}
	native, err := library.native.open(selector)
	if err != nil {
		return nil, err
	}
	if native == nil {
		return nil, errors.New("D2XX native open returned a nil device")
	}
	library.open++
	return &Device{native: native, release: library.releaseDevice}, nil
}

func (library *Library) releaseDevice() {
	library.mu.Lock()
	if library.open > 0 {
		library.open--
	}
	library.mu.Unlock()
}

func (library *Library) Close() error {
	if library == nil {
		return nil
	}
	library.mu.Lock()
	defer library.mu.Unlock()
	if library.native == nil {
		return nil
	}
	if library.open != 0 {
		return fmt.Errorf("%w: %d", ErrDevicesOpen, library.open)
	}
	err := library.native.close()
	if err == nil {
		library.native = nil
	}
	return err
}

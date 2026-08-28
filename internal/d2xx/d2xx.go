// Package d2xx provides the narrow native FTDI D2XX boundary used by
// IWR6843 capture builds. Loading the library does not enumerate or open devices.
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

type Selector struct {
	Description string
}

func (selector Selector) Validate() error {
	if selector.Description == "" {
		return errors.New("D2XX description is empty")
	}
	if strings.ContainsRune(selector.Description, 0) {
		return errors.New("D2XX description contains NUL")
	}
	return nil
}

type nativeLibrary interface {
	open(Selector) (nativeDevice, error)
	close() error
}

type Library struct {
	mu     sync.Mutex
	native nativeLibrary
	open   int
}

func Load() (*Library, error) {
	native, err := openNative()
	if err != nil {
		return nil, err
	}
	return &Library{native: native}, nil
}

func (library *Library) Open(selector Selector) (*Device, error) {
	if err := selector.Validate(); err != nil {
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

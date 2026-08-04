//go:build windows

package serialport

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	dcbBinary    = 1 << 0
	purgeRXClear = 0x0008
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetCommState    = kernel32.NewProc("GetCommState")
	procSetCommState    = kernel32.NewProc("SetCommState")
	procSetCommTimeouts = kernel32.NewProc("SetCommTimeouts")
	procPurgeComm       = kernel32.NewProc("PurgeComm")
)

type windowsPort struct {
	file      *os.File
	handle    syscall.Handle
	timeoutMu sync.Mutex
	timeouts  commTimeouts
}

func (port *windowsPort) Read(buffer []byte) (int, error) {
	count, err := port.file.Read(buffer)
	// A Win32 COM timeout is ReadFile success with zero bytes. os.File's
	// internal poll descriptor translates that result to io.EOF because it
	// cannot distinguish a serial handle from a regular stream. Restore the
	// native empty-read meaning so the radar client can keep polling until its
	// absolute command/context deadline.
	if count == 0 && errors.Is(err, io.EOF) {
		return 0, nil
	}
	return count, err
}
func (port *windowsPort) Write(buffer []byte) (int, error) { return port.file.Write(buffer) }
func (port *windowsPort) Close() error                     { return port.file.Close() }

func (port *windowsPort) SetReadDeadline(deadline time.Time) error {
	port.timeoutMu.Lock()
	defer port.timeoutMu.Unlock()
	port.timeouts.readTotalConstant = deadlineMilliseconds(deadline)
	if err := callBool(procSetCommTimeouts, uintptr(port.handle), uintptr(unsafe.Pointer(&port.timeouts))); err != nil {
		return fmt.Errorf("set serial read deadline: %w", err)
	}
	return nil
}

func (port *windowsPort) SetWriteDeadline(deadline time.Time) error {
	port.timeoutMu.Lock()
	defer port.timeoutMu.Unlock()
	port.timeouts.writeTotalConstant = deadlineMilliseconds(deadline)
	if err := callBool(procSetCommTimeouts, uintptr(port.handle), uintptr(unsafe.Pointer(&port.timeouts))); err != nil {
		return fmt.Errorf("set serial write deadline: %w", err)
	}
	return nil
}

func (port *windowsPort) PurgeInput() error {
	if err := callBool(procPurgeComm, uintptr(port.handle), purgeRXClear); err != nil {
		return fmt.Errorf("clear serial receive buffer: %w", err)
	}
	return nil
}

// dcb mirrors the Win32 DCB layout. The flags field contains the C bitfields
// from fBinary through fDummy2.
type dcb struct {
	length      uint32
	baudRate    uint32
	flags       uint32
	reserved    uint16
	xonLimit    uint16
	xoffLimit   uint16
	byteSize    byte
	parity      byte
	stopBits    byte
	xonChar     int8
	xoffChar    int8
	errorChar   int8
	eofChar     int8
	eventChar   int8
	reservedOne uint16
}

type commTimeouts struct {
	readInterval         uint32
	readTotalMultiplier  uint32
	readTotalConstant    uint32
	writeTotalMultiplier uint32
	writeTotalConstant   uint32
}

func openPlatform(name string, baud int, timeout time.Duration) (Port, error) {
	deviceName, err := normalizeWindowsName(name)
	if err != nil {
		return nil, err
	}

	namePointer, err := syscall.UTF16PtrFromString(deviceName)
	if err != nil {
		return nil, fmt.Errorf("encode serial port name %q: %w", name, err)
	}

	handle, err := syscall.CreateFile(
		namePointer,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open serial port %q: %w", name, err)
	}

	closeOnError := true
	defer func() {
		if closeOnError {
			_ = syscall.CloseHandle(handle)
		}
	}()

	config := dcb{length: uint32(unsafe.Sizeof(dcb{}))}
	if err := callBool(procGetCommState, uintptr(handle), uintptr(unsafe.Pointer(&config))); err != nil {
		return nil, fmt.Errorf("read serial port %q configuration: %w", name, err)
	}

	// Reset all flow-control, parity, DTR and RTS flags. fBinary is mandatory
	// on Windows. The remaining fields select 8 data bits, no parity and one
	// stop bit.
	config.baudRate = uint32(baud)
	config.flags = dcbBinary
	config.byteSize = 8
	config.parity = 0
	config.stopBits = 0
	if err := callBool(procSetCommState, uintptr(handle), uintptr(unsafe.Pointer(&config))); err != nil {
		return nil, fmt.Errorf("configure serial port %q as 8N1: %w", name, err)
	}

	timeoutMilliseconds := durationCeil(timeout, time.Millisecond)
	timeouts := commTimeouts{
		// This documented combination returns available input immediately and
		// otherwise waits readTotalConstant for the first byte.
		readInterval:        ^uint32(0),
		readTotalMultiplier: ^uint32(0),
		readTotalConstant:   timeoutMilliseconds,
		writeTotalConstant:  timeoutMilliseconds,
	}
	if err := callBool(procSetCommTimeouts, uintptr(handle), uintptr(unsafe.Pointer(&timeouts))); err != nil {
		return nil, fmt.Errorf("set serial port %q timeouts: %w", name, err)
	}

	file := os.NewFile(uintptr(handle), deviceName)
	if file == nil {
		return nil, fmt.Errorf("wrap serial port %q handle", name)
	}

	closeOnError = false
	return &windowsPort{file: file, handle: handle, timeouts: timeouts}, nil
}

func normalizeWindowsName(name string) (string, error) {
	const devicePrefix = `\\.\`

	portName := name
	if strings.HasPrefix(portName, devicePrefix) {
		portName = portName[len(devicePrefix):]
	}
	if len(portName) >= 3 && strings.EqualFold(portName[:3], "COM") {
		portName = portName[3:]
	}
	if portName == "" {
		return "", fmt.Errorf("invalid Windows serial port name %q", name)
	}

	portNumber, err := strconv.ParseUint(portName, 10, 32)
	if err != nil || portNumber == 0 {
		return "", fmt.Errorf("invalid Windows serial port name %q", name)
	}

	// The Win32 device namespace form is valid for every COM number and is
	// required for COM10 and above.
	return devicePrefix + "COM" + strconv.FormatUint(portNumber, 10), nil
}

func callBool(proc *syscall.LazyProc, arguments ...uintptr) error {
	result, _, callError := proc.Call(arguments...)
	if result != 0 {
		return nil
	}
	if callError != nil && callError != syscall.Errno(0) {
		return callError
	}
	return syscall.EINVAL
}

func durationCeil(value, unit time.Duration) uint32 {
	return uint32((value + unit - 1) / unit)
}

func deadlineMilliseconds(deadline time.Time) uint32 {
	if deadline.IsZero() {
		return durationCeil(maxReadTimeout, time.Millisecond)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 1
	}
	if remaining > maxReadTimeout {
		remaining = maxReadTimeout
	}
	return durationCeil(remaining, time.Millisecond)
}

//go:build linux && (amd64 || arm64)

package serialport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	linuxTCFLSH   = 0x540B
	linuxTIOCEXCL = 0x540C
	linuxTCIFLUSH = 0
)

type linuxPort struct {
	file *os.File
	fd   int
}

func (port *linuxPort) Read(buffer []byte) (int, error)  { return port.file.Read(buffer) }
func (port *linuxPort) Write(buffer []byte) (int, error) { return port.file.Write(buffer) }
func (port *linuxPort) Close() error                     { return port.file.Close() }
func (port *linuxPort) SetReadDeadline(deadline time.Time) error {
	return port.file.SetReadDeadline(deadline)
}
func (port *linuxPort) SetWriteDeadline(deadline time.Time) error {
	return port.file.SetWriteDeadline(deadline)
}

func (port *linuxPort) PurgeInput() error {
	if err := ioctlValue(port.fd, linuxTCFLSH, linuxTCIFLUSH); err != nil {
		return fmt.Errorf("clear serial receive buffer: %w", err)
	}
	return nil
}

func openPlatform(name string, baud int, timeout time.Duration) (Port, error) {
	deviceName, err := normalizeLinuxName(name)
	if err != nil {
		return nil, err
	}

	speed, err := linuxBaud(baud)
	if err != nil {
		return nil, err
	}

	fd, err := syscall.Open(deviceName, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open serial port %q: %w", name, err)
	}

	closeOnError := true
	defer func() {
		if closeOnError {
			_ = syscall.Close(fd)
		}
	}()
	if err := ioctlValue(fd, linuxTIOCEXCL, 0); err != nil {
		return nil, fmt.Errorf("lock serial port %q exclusively: %w", name, err)
	}

	var settings syscall.Termios
	if err := ioctlTermios(fd, syscall.TCGETS, &settings); err != nil {
		return nil, fmt.Errorf("read serial port %q configuration: %w", name, err)
	}

	// Raw 8N1, local receiver enabled, with parity and hardware/software flow
	// control disabled. Baud constants are encoded in Cflag on Linux.
	settings.Iflag = syscall.IGNPAR
	settings.Oflag = 0
	settings.Cflag = speed | syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	settings.Lflag = 0
	settings.Line = 0
	for index := range settings.Cc {
		settings.Cc[index] = 0
	}
	// VMIN=1 keeps an empty nonblocking tty read in EAGAIN. os.NewFile can then
	// use the runtime poller and SetReadDeadline correctly. VMIN=0 would make
	// the kernel return zero immediately, which os.File translates to io.EOF.
	settings.Cc[syscall.VMIN] = 1
	settings.Cc[syscall.VTIME] = 0
	settings.Ispeed = speed
	settings.Ospeed = speed

	if err := ioctlTermios(fd, syscall.TCSETS, &settings); err != nil {
		return nil, fmt.Errorf("configure serial port %q as 8N1: %w", name, err)
	}

	file := os.NewFile(uintptr(fd), deviceName)
	if file == nil {
		return nil, fmt.Errorf("wrap serial port %q file descriptor", name)
	}
	// os.File owns fd from this point. Disable the raw-fd defer immediately so
	// an initialization failure cannot close a descriptor twice after a file
	// finalizer observes it.
	closeOnError = false
	if err := file.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("enable bounded serial I/O for %q: %w", name, err)
	}
	if err := file.SetDeadline(time.Time{}); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("clear initial serial deadline for %q: %w", name, err)
	}

	return &linuxPort{file: file, fd: fd}, nil
}

func normalizeLinuxName(name string) (string, error) {
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	if strings.ContainsRune(name, filepath.Separator) || !strings.HasPrefix(name, "tty") {
		return "", fmt.Errorf("invalid Linux serial port name %q; use /dev/tty* or tty*", name)
	}
	return filepath.Join("/dev", name), nil
}

func linuxBaud(baud int) (uint32, error) {
	switch baud {
	case 9600:
		return syscall.B9600, nil
	case 19200:
		return syscall.B19200, nil
	case 38400:
		return syscall.B38400, nil
	case 57600:
		return syscall.B57600, nil
	case 115200:
		return syscall.B115200, nil
	case 230400:
		return syscall.B230400, nil
	case 460800:
		return syscall.B460800, nil
	case 500000:
		return syscall.B500000, nil
	case 576000:
		return syscall.B576000, nil
	case 921600:
		return syscall.B921600, nil
	default:
		return 0, fmt.Errorf("unsupported Linux serial baud: %d", baud)
	}
}

func ioctlTermios(fd int, operation uint, settings *syscall.Termios) error {
	_, _, callError := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(operation),
		uintptr(unsafe.Pointer(settings)),
	)
	if callError != 0 {
		return callError
	}
	return nil
}

func ioctlValue(fd int, operation uint, value uintptr) error {
	_, _, callError := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(operation),
		value,
	)
	if callError != 0 {
		return callError
	}
	return nil
}

func durationCeil(value, unit time.Duration) uint32 {
	return uint32((value + unit - 1) / unit)
}

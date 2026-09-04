//go:build linux

package serialport

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

type linuxPort struct {
	file *os.File
}

const (
	linuxTCFLSH   = 0x540B
	linuxTCIFLUSH = 0
)

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
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, port.file.Fd(), linuxTCFLSH, linuxTCIFLUSH)
	if errno != 0 {
		return fmt.Errorf("clear serial receive buffer: %w", errno)
	}
	return nil
}

func openPlatform(name string, baud int, _ time.Duration) (Port, error) {
	baudFlag, ok := linuxBaud[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported Linux serial baud: %d", baud)
	}
	file, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open serial port %q: %w", name, err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()

	var termios syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&termios))); errno != 0 {
		return nil, fmt.Errorf("read serial port %q configuration: %w", name, errno)
	}
	termios.Iflag = 0
	termios.Oflag = 0
	termios.Lflag = 0
	termios.Cflag = uint32(baudFlag) | syscall.CS8 | syscall.CLOCAL | syscall.CREAD
	termios.Ispeed = baudFlag
	termios.Ospeed = baudFlag
	termios.Cc[syscall.VMIN] = 0
	termios.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&termios))); errno != 0 {
		return nil, fmt.Errorf("configure serial port %q as 8N1: %w", name, errno)
	}

	closeOnError = false
	return &linuxPort{file: file}, nil
}

var linuxBaud = map[int]uint32{
	9600:   syscall.B9600,
	19200:  syscall.B19200,
	38400:  syscall.B38400,
	57600:  syscall.B57600,
	115200: syscall.B115200,
	230400: syscall.B230400,
	460800: syscall.B460800,
	921600: syscall.B921600,
}

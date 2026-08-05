// Package serialport provides the small, dependency-free serial transport used
// by mmwcli. Platform implementations configure ports as raw 8N1 with no flow
// control.
package serialport

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const maxReadTimeout = 25500 * time.Millisecond

// Port is an opened serial port.
type Port interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	PurgeInput() error
}

// Open opens name at baud and verifies a bounded I/O timeout. Protocol callers
// set operation-specific deadlines through Port before each read or write.
func Open(name string, baud int, timeout time.Duration) (Port, error) {
	if err := validateOptions(name, baud, timeout); err != nil {
		return nil, err
	}

	return openPlatform(name, baud, timeout)
}

func validateOptions(name string, baud int, timeout time.Duration) error {
	if name == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("serial port name is empty")
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("serial port name %q has leading or trailing whitespace", name)
	}
	if strings.IndexByte(name, 0) >= 0 {
		return fmt.Errorf("serial port name contains NUL")
	}
	if baud <= 0 {
		return fmt.Errorf("serial baud must be positive: %d", baud)
	}
	if uint64(baud) > uint64(^uint32(0)) {
		return fmt.Errorf("serial baud is too large: %d", baud)
	}
	if timeout <= 0 {
		return fmt.Errorf("serial timeout must be positive: %s", timeout)
	}
	if timeout > maxReadTimeout {
		return fmt.Errorf("serial timeout %s exceeds maximum %s", timeout, maxReadTimeout)
	}

	return nil
}

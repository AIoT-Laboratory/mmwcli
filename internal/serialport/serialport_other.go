//go:build !windows && !linux

package serialport

import (
	"errors"
	"time"
)

func openPlatform(string, int, time.Duration) (Port, error) {
	return nil, errors.New("serial ports are unsupported on this platform")
}

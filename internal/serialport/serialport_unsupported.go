//go:build !windows && !(linux && (amd64 || arm64))

package serialport

import (
	"fmt"
	"runtime"
	"time"
)

func openPlatform(_ string, _ int, _ time.Duration) (Port, error) {
	return nil, fmt.Errorf("serial ports are unsupported on %s", runtime.GOOS)
}

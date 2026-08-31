//go:build !windows

package d2xx

import (
	"errors"
)

func openNative() (nativeLibrary, error) {
	return nil, errors.New("FTDI D2XX backend requires Windows")
}

//go:build !windows && !linux

package d2xx

import (
	"errors"
)

func openNative() (nativeLibrary, error) {
	return nil, errors.New("FTDI D2XX backend is unsupported on this platform")
}

//go:build windows && !ftd2xx

package d2xx

import (
	"errors"
)

func openNative() (nativeLibrary, error) {
	return nil, errors.New("FTDI D2XX backend requires the Windows ftd2xx build tag")
}

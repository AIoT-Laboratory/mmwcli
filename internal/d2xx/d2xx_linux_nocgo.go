//go:build linux && !cgo

package d2xx

import "errors"

func openNative() (nativeLibrary, error) {
	return nil, errors.New("FTDI D2XX on Linux requires a CGO-enabled build and libftd2xx.so")
}

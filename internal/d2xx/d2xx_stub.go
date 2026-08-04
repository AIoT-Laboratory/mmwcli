//go:build !ftd2xx || (linux && !cgo) || (!windows && !linux)

package d2xx

import (
	"fmt"
	"runtime"
)

func openNative() (nativeLibrary, error) {
	return nil, fmt.Errorf(
		"FTDI D2XX backend is unavailable in this %s/%s build; use -tags ftd2xx with CGO_ENABLED=0 on Windows or CGO_ENABLED=1 on Linux",
		runtime.GOOS,
		runtime.GOARCH,
	)
}

//go:build linux && cgo && ftd2xx

package d2xx

/*
#cgo LDFLAGS: -lftd2xx
#include <stdint.h>

typedef uint32_t FT_STATUS;
typedef FT_STATUS (*mmwcli_create_device_info_list_fn)(uint32_t *);

extern FT_STATUS FT_CreateDeviceInfoList(uint32_t *numberOfDevices);

static mmwcli_create_device_info_list_fn volatile mmwcli_d2xx_anchor = FT_CreateDeviceInfoList;

static int mmwcli_d2xx_linked(void) {
	return mmwcli_d2xx_anchor != 0;
}
*/
import "C"

import "errors"

type linuxLibrary struct{}

func openNative() (nativeLibrary, error) {
	if C.mmwcli_d2xx_linked() == 0 {
		return nil, errors.New("FTDI D2XX library symbol FT_CreateDeviceInfoList is unavailable")
	}
	return &linuxLibrary{}, nil
}

func (*linuxLibrary) info() (nativeInfo, error) {
	return nativeInfo{library: "libftd2xx.so"}, nil
}

func (*linuxLibrary) close() error { return nil }

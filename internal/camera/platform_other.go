//go:build !windows && !linux

package camera

import (
	"context"
	"errors"
)

func cameraCommand(Config, bool) []string {
	return nil
}

func listDevices(context.Context) ([]Device, error) {
	return nil, errors.New("camera capture is unsupported on this platform")
}

//go:build windows

package camera

import (
	"context"
	"strconv"
)

func cameraCommand(config Config, oneFrame bool) []string {
	argv := []string{
		Executable, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "dshow",
		"-video_size", strconv.Itoa(config.Width) + "x" + strconv.Itoa(config.Height),
		"-framerate", strconv.Itoa(config.FPS),
		"-i", "video=" + config.Device,
		"-an", "-c:v", "mjpeg",
	}
	if oneFrame {
		argv = append(argv, "-frames:v", "1")
	}
	return append(argv, "-f", "image2pipe", "pipe:1")
}

func listDevices(ctx context.Context) ([]Device, error) {
	return listDirectShowDevices(ctx)
}

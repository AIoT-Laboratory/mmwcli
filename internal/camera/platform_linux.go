//go:build linux

package camera

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func cameraCommand(config Config, oneFrame bool) []string {
	argv := []string{
		Executable, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "v4l2",
		"-video_size", strconv.Itoa(config.Width) + "x" + strconv.Itoa(config.Height),
		"-framerate", strconv.Itoa(config.FPS),
		"-i", config.Device,
		"-an", "-c:v", "mjpeg",
	}
	if oneFrame {
		argv = append(argv, "-frames:v", "1")
	}
	return append(argv, "-f", "image2pipe", "pipe:1")
}

func listDevices(ctx context.Context) ([]Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list V4L2 cameras: %w", err)
	}
	paths, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, fmt.Errorf("list V4L2 cameras: %w", err)
	}
	sort.Strings(paths)
	devices := make([]Device, 0, len(paths))
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode()&os.ModeDevice == 0 {
			continue
		}
		name := filepath.Base(path)
		sysfsName := filepath.Join("/sys/class/video4linux", name, "name")
		if value, readErr := os.ReadFile(sysfsName); readErr == nil && strings.TrimSpace(string(value)) != "" {
			name = strings.TrimSpace(string(value))
		}
		devices = append(devices, Device{Name: name, ID: path})
	}
	if len(devices) == 0 {
		return nil, errors.New("V4L2 reported no video cameras")
	}
	return devices, nil
}

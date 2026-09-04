//go:build linux

package camera

import (
	"slices"
	"testing"
)

func TestLinuxCameraCommandUsesV4L2Device(t *testing.T) {
	config := Config{Device: "/dev/video2", Width: 1280, Height: 720, FPS: 30}
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "v4l2",
		"-video_size", "1280x720", "-framerate", "30", "-i", "/dev/video2",
		"-an", "-c:v", "mjpeg", "-frames:v", "1", "-f", "image2pipe", "pipe:1",
	}
	if got := config.command(true); !slices.Equal(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

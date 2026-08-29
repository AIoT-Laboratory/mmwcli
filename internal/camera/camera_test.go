package camera

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
)

func TestReadJPEGDoesNotSplitStuffedEntropyBytes(t *testing.T) {
	want := []byte{
		0xff, 0xd8,
		0xff, 0xe0, 0x00, 0x02,
		0xff, 0xda, 0x00, 0x02,
		0x01, 0xff, 0x00, 0x02,
		0xff, 0xd9,
	}
	got, err := readJPEG(bufio.NewReader(bytes.NewReader(want)), len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("JPEG differs: %x", got)
	}
}

func TestCameraIndexIsMinimalAndContiguous(t *testing.T) {
	encoded, err := encodeIndex([]entry{
		{offset: 0, size: 10, receivedNS: 100},
		{offset: 10, size: 12, receivedNS: 140},
	}, 22)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded[:8]) != indexMagic || len(encoded) != indexHeaderBytes+2*indexEntryBytes {
		t.Fatalf("unexpected camera index: %x", encoded)
	}
	if got := binary.LittleEndian.Uint64(encoded[16:24]); got != 2 {
		t.Fatalf("item count = %d", got)
	}
	if _, err := encodeIndex([]entry{{offset: 1, size: 10}}, 10); err == nil {
		t.Fatal("non-contiguous index accepted")
	}
}

func TestConfigBuildsTheOnlyCaptureCommand(t *testing.T) {
	config := Config{Device: "@device_pnp_camera", Width: 1280, Height: 720, FPS: 30, MaxBytes: 1024}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	got := config.command(false)
	want := []string{
		"ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "dshow",
		"-video_size", "1280x720", "-framerate", "30", "-i", "video=@device_pnp_camera",
		"-an", "-c:v", "mjpeg", "-f", "image2pipe", "pipe:1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
	if got := config.command(true); !slices.Contains(got, "-frames:v") {
		t.Fatalf("preview command lacks one-frame limit: %#v", got)
	}
}

func TestParseDirectShowDevicesSelectsOnlyVideoAndAlternativeNames(t *testing.T) {
	output := []byte(`
[dshow @ 000001] DirectShow video devices (some may be both video and audio devices)
[dshow @ 000001]  "RGB Camera" (video)
[dshow @ 000001]     Alternative name "@device_pnp_rgb"
[dshow @ 000001]  "Unavailable" (none)
[dshow @ 000001] DirectShow audio devices
[dshow @ 000001]  "Microphone" (audio)
[dshow @ 000001]     Alternative name "@device_pnp_mic"
`)
	got := parseDirectShowDevices(output)
	want := []Device{
		{Name: "RGB Camera", ID: "@device_pnp_rgb"},
		{Name: "Unavailable", ID: "Unavailable"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("devices = %#v, want %#v", got, want)
	}
}

func TestParseDirectShowDevicesAcceptsFFmpeg9NoHeaderNoneDevice(t *testing.T) {
	output := []byte(`
[dshow @ 000001]  "Integrated Camera" (none)
[dshow @ 000001]     Alternative name "@device_pnp_integrated"
[dshow @ 000001] Could not enumerate audio only devices
`)
	want := []Device{{Name: "Integrated Camera", ID: "@device_pnp_integrated"}}
	if got := parseDirectShowDevices(output); !slices.Equal(got, want) {
		t.Fatalf("devices = %#v, want %#v", got, want)
	}
}

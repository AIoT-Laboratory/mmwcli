package camera

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"slices"
	"testing"
	"time"
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

func TestRecorderCancellationIgnoresOnlyExpectedPipeTermination(t *testing.T) {
	cameraFailure := errors.New("camera shutdown failed")
	for _, test := range []struct {
		name       string
		cancel     bool
		readErr    error
		wantFinish error
	}{
		{name: "cancelled EOF", cancel: true, readErr: io.EOF},
		{name: "cancelled partial JPEG", cancel: true, readErr: io.ErrUnexpectedEOF},
		{name: "cancelled closed pipe", cancel: true, readErr: io.ErrClosedPipe},
		{name: "cancelled closed file", cancel: true, readErr: os.ErrClosed},
		{name: "camera failure during cancellation", cancel: true, readErr: cameraFailure, wantFinish: cameraFailure},
		{name: "camera EOF without cancellation", readErr: io.EOF, wantFinish: io.EOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancel {
				cancel()
			}
			recorder := &Recorder{
				baseCtx: ctx,
				cancel:  cancel,
				frames:  make(chan frame, 1),
				done:    make(chan struct{}),
				ready:   make(chan error, 1),
			}
			recorder.frames <- frame{err: test.readErr}
			close(recorder.frames)
			recorder.write()

			finishErr := recorder.Finish(context.Background(), false)
			if test.wantFinish == nil && finishErr != nil {
				t.Fatalf("Finish error = %v, want nil", finishErr)
			}
			if test.wantFinish != nil && !errors.Is(finishErr, test.wantFinish) {
				t.Fatalf("Finish error = %v, want %v", finishErr, test.wantFinish)
			}
			select {
			case <-recorder.done:
			case <-time.After(time.Second):
				t.Fatal("camera writer did not finish")
			}
		})
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

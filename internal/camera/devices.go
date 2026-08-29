package camera

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Device identifies one DirectShow video input. ID is its alternative name when
// FFmpeg exposes one, which avoids ambiguity between equal friendly names.
type Device struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

var (
	dshowDeviceLine  = regexp.MustCompile(`^\s*"(.+)"(?:\s+\(([^)]*)\))?\s*$`)
	dshowAlternative = regexp.MustCompile(`^\s*Alternative name "(.+)"\s*$`)
)

// List asks FFmpeg's DirectShow input for its currently visible video
// devices. FFmpeg exits non-zero after a successful -list_devices invocation.
func List(ctx context.Context) ([]Device, error) {
	output, err := exec.CommandContext(
		ctx, Executable, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy",
	).CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("list DirectShow cameras: %w", ctxErr)
	}
	devices := parseDirectShowDevices(output)
	if len(devices) != 0 {
		return devices, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list DirectShow cameras: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil, errors.New("DirectShow reported no video cameras")
}

// Preview returns one complete JPEG from the same generated FFmpeg DirectShow
// input command that Recorder uses, with a one-frame output limit.
func Preview(ctx context.Context, config Config) ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	argv := config.command(true)
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open camera preview stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start camera preview: %w", err)
	}
	image, readErr := readJPEG(bufio.NewReaderSize(stdout, 64<<10), config.MaxBytes)
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = stdout.Close()
	_ = command.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("camera preview: %w", ctxErr)
	}
	if readErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("read camera preview: %w: %s", readErr, message)
		}
		return nil, fmt.Errorf("read camera preview: %w", readErr)
	}
	return image, nil
}

// parseDirectShowDevices parses both classic FFmpeg output (separate video
// section) and current output carrying media types such as "(video)" or
// "(none)". FFmpeg 9 reports video devices as "(none)" without a header, so
// that status retains the active section; explicit audio entries are ignored.
func parseDirectShowDevices(output []byte) []Device {
	var devices []Device
	// FFmpeg 9 may omit the legacy "DirectShow video devices" header. Device
	// rows precede its audio-only diagnostic, so begin in the video section.
	inVideoSection := true
	pending := -1
	for scanner := bufio.NewScanner(bytes.NewReader(output)); scanner.Scan(); {
		line := dshowMessage(scanner.Text())
		switch {
		case strings.Contains(line, "DirectShow video devices"):
			inVideoSection = true
			pending = -1
			continue
		case strings.Contains(line, "DirectShow audio devices"), strings.Contains(line, "audio only devices"):
			inVideoSection = false
			pending = -1
			continue
		}
		if match := dshowAlternative.FindStringSubmatch(line); match != nil {
			if pending >= 0 {
				devices[pending].ID = match[1]
			}
			continue
		}
		match := dshowDeviceLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		explicitVideo := false
		explicitAudio := false
		for _, value := range strings.Split(match[2], ",") {
			switch strings.TrimSpace(value) {
			case "video":
				explicitVideo = true
			case "audio":
				explicitAudio = true
			}
		}
		isVideo := inVideoSection
		if explicitVideo {
			isVideo = true
		} else if explicitAudio {
			isVideo = false
		}
		if !isVideo {
			pending = -1
			continue
		}
		devices = append(devices, Device{Name: match[1], ID: match[1]})
		pending = len(devices) - 1
	}
	return devices
}

func dshowMessage(line string) string {
	if close := strings.Index(line, "]"); close >= 0 {
		return strings.TrimSpace(line[close+1:])
	}
	return strings.TrimSpace(line)
}

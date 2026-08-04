//go:build linux && (amd64 || arm64)

package serialport

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestNormalizeLinuxName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "/dev/ttyUSB0", want: "/dev/ttyUSB0"},
		{input: "/dev/serial/by-id/example", want: "/dev/serial/by-id/example"},
		{input: "ttyACM0", want: "/dev/ttyACM0"},
		{input: "COM3", wantErr: true},
		{input: "relative/device", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := normalizeLinuxName(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalizeLinuxName(%q) = %q, want error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLinuxName(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalizeLinuxName(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestLinuxBaud(t *testing.T) {
	t.Parallel()

	for baud, want := range map[int]uint32{
		115200: syscall.B115200,
		921600: syscall.B921600,
	} {
		got, err := linuxBaud(baud)
		if err != nil {
			t.Fatalf("linuxBaud(%d) error = %v", baud, err)
		}
		if got != want {
			t.Fatalf("linuxBaud(%d) = %#x, want %#x", baud, got, want)
		}
	}
	if _, err := linuxBaud(12345); err == nil {
		t.Fatal("linuxBaud(12345) succeeded, want error")
	}
}

func TestDurationCeilLinuxDeciseconds(t *testing.T) {
	t.Parallel()

	if got := durationCeil(time.Millisecond, 100*time.Millisecond); got != 1 {
		t.Fatalf("durationCeil() = %d, want 1", got)
	}
	if got := durationCeil(101*time.Millisecond, 100*time.Millisecond); got != 2 {
		t.Fatalf("durationCeil() = %d, want 2", got)
	}
}

func TestLinuxPTYReadHonorsDeadlineAndDelayedInput(t *testing.T) {
	master, slaveName := openLinuxPTY(t)
	defer master.Close()

	port, err := openPlatform(slaveName, 115200, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()

	started := time.Now()
	if err := port.SetReadDeadline(started.Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	count, err := port.Read(make([]byte, 16))
	if count != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("empty PTY read = (%d, %v), want deadline error", count, err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("empty PTY read returned too early after %s", elapsed)
	}

	if err := port.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	writeResult := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, writeErr := master.Write([]byte("Done\n"))
		writeResult <- writeErr
	}()
	buffer := make([]byte, 16)
	count, err = port.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buffer[:count]); got != "Done\n" {
		t.Fatalf("delayed PTY data = %q, want Done\\n", got)
	}
	if err := <-writeResult; err != nil {
		t.Fatal(err)
	}
}

func openLinuxPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	const (
		tiocsptlck = 0x40045431
		tiocgptn   = 0x80045430
	)
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("Linux PTY unavailable: %v", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = syscall.Close(fd)
		}
	}()
	unlock := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocsptlck, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Skipf("unlock Linux PTY: %v", errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tiocgptn, uintptr(unsafe.Pointer(&number))); errno != 0 {
		t.Skipf("query Linux PTY number: %v", errno)
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	if master == nil {
		t.Fatal("wrap Linux PTY master")
	}
	closeOnError = false
	return master, fmt.Sprintf("/dev/pts/%d", number)
}

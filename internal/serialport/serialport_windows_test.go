//go:build windows

package serialport

import (
	"os"
	"testing"
	"time"
	"unsafe"
)

func TestNormalizeWindowsName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "3", want: `\\.\COM3`},
		{input: "COM3", want: `\\.\COM3`},
		{input: "com10", want: `\\.\COM10`},
		{input: `\\.\COM10`, want: `\\.\COM10`},
		{input: "COM0007", want: `\\.\COM7`},
		{input: "COM0", wantErr: true},
		{input: "COM", wantErr: true},
		{input: "ttyUSB0", wantErr: true},
		{input: `\\?\COM3`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := normalizeWindowsName(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalizeWindowsName(%q) = %q, want error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeWindowsName(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalizeWindowsName(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestDurationCeilWindowsMilliseconds(t *testing.T) {
	t.Parallel()

	if got := durationCeil(100*time.Microsecond, time.Millisecond); got != 1 {
		t.Fatalf("durationCeil() = %d, want 1", got)
	}
}

func TestWindowsPortRestoresZeroByteEOFAsEmptyRead(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	port := &windowsPort{file: reader}
	count, err := port.Read(make([]byte, 1))
	if err != nil || count != 0 {
		t.Fatalf("empty COM-style read = (%d, %v), want (0, nil)", count, err)
	}
}

func TestWindowsStructureSizes(t *testing.T) {
	t.Parallel()

	if got := unsafe.Sizeof(dcb{}); got != 28 {
		t.Fatalf("DCB size = %d, want 28", got)
	}
	if got := unsafe.Sizeof(commTimeouts{}); got != 20 {
		t.Fatalf("COMMTIMEOUTS size = %d, want 20", got)
	}
}

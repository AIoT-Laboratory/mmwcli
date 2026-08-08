package serialport

import (
	"strings"
	"testing"
	"time"
)

func TestValidateOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		port    string
		baud    int
		timeout time.Duration
		want    string
	}{
		{name: "valid low baud", port: "COM3", baud: 115200, timeout: time.Second},
		{name: "valid studio_cli UART", port: "COM10", baud: 921600, timeout: 2500 * time.Millisecond},
		{name: "empty name", baud: 115200, timeout: time.Second, want: "name is empty"},
		{name: "blank name", port: "  ", baud: 115200, timeout: time.Second, want: "name is empty"},
		{name: "surrounding whitespace", port: " COM3", baud: 115200, timeout: time.Second, want: "whitespace"},
		{name: "NUL in name", port: "COM\x003", baud: 115200, timeout: time.Second, want: "NUL"},
		{name: "zero baud", port: "COM3", timeout: time.Second, want: "baud must be positive"},
		{name: "negative baud", port: "COM3", baud: -1, timeout: time.Second, want: "baud must be positive"},
		{name: "zero timeout", port: "COM3", baud: 115200, want: "timeout must be positive"},
		{name: "negative timeout", port: "COM3", baud: 115200, timeout: -time.Millisecond, want: "timeout must be positive"},
		{name: "timeout too large", port: "COM3", baud: 115200, timeout: maxReadTimeout + time.Nanosecond, want: "exceeds maximum"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateOptions(test.port, test.baud, test.timeout)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validateOptions() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOptions() error = %v, want text %q", err, test.want)
			}
		})
	}
}

//go:build windows

package capturefile

import (
	"strings"
	"testing"
)

func TestWindowsAPIPath(t *testing.T) {
	t.Parallel()

	if got := windowsAPIPath(`C:\capture.bin`); got != `C:\capture.bin` {
		t.Fatalf("short path = %q", got)
	}
	local := `C:\` + strings.Repeat("a", 248)
	if got, want := windowsAPIPath(local), `\\?\`+local; got != want {
		t.Fatalf("long local path = %q, want %q", got, want)
	}
	unc := `\\server\share\` + strings.Repeat("b", 248)
	if got, want := windowsAPIPath(unc), `\\?\UNC\`+unc[2:]; got != want {
		t.Fatalf("long UNC path = %q, want %q", got, want)
	}
}

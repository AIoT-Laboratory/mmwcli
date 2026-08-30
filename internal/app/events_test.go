package app

import (
	"bytes"
	"testing"
)

func TestSessionLogEmitsOneStableRadarStartedEvent(t *testing.T) {
	var stderr bytes.Buffer
	log := sessionLog(&stderr, "stream")
	log("DCA1000 recording started")
	log("radar started")

	want := "[stream] DCA1000 recording started\n" +
		"[stream] radar started\n" +
		radarStartedEvent + "\n"
	if got := stderr.String(); got != want {
		t.Fatalf("session stderr = %q, want %q", got, want)
	}
}

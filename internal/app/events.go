package app

import (
	"fmt"
	"io"
)

const radarStartedEvent = `MMWCLI_EVENT {"event":"radar_started"}`

func sessionLog(stderr io.Writer, scope string) func(string) {
	return func(message string) {
		fmt.Fprintf(stderr, "[%s] %s\n", scope, message)
		if message == "radar started" {
			fmt.Fprintln(stderr, radarStartedEvent)
		}
	}
}

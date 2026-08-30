package app

import (
	"bufio"
	"context"
	"io"
)

func watchManagedStop(control io.Reader, cancel context.CancelFunc) {
	reader := bufio.NewReader(control)
	for {
		line, err := reader.ReadString('\n')
		if line == "stop\n" || err != nil {
			cancel()
			return
		}
	}
}

package app

import (
	"errors"
	"fmt"

	"mmwcli/internal/session"
)

type captureClientCloser interface {
	Close() error
}

func closeCaptureClient(
	resultErr error,
	outputCommitted bool,
	resource string,
	closer captureClientCloser,
) error {
	closeErr := closer.Close()
	if closeErr == nil {
		return resultErr
	}
	operation := "close " + resource
	if outputCommitted {
		operation = "post-commit cleanup: " + operation
	}
	marked := &session.CleanupError{Err: fmt.Errorf("%s: %w", operation, closeErr)}
	return errors.Join(resultErr, marked)
}

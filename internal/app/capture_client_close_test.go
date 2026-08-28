package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/session"
)

func TestCloseCaptureClientReportsPostCommitFailure(t *testing.T) {
	for _, resource := range []string{"radar client", "DCA1000 control client"} {
		t.Run(resource, func(t *testing.T) {
			closeCause := errors.New("injected close failure")
			closer := &injectedCaptureCloser{err: closeCause}
			err := closeCaptureClient(nil, true, resource, closer)
			var cleanup *session.CleanupError
			if closer.calls != 1 || !errors.Is(err, closeCause) || !errors.As(err, &cleanup) {
				t.Fatalf("close result = (%d calls, %v)", closer.calls, err)
			}
			if !strings.Contains(err.Error(), "post-commit cleanup") || !strings.Contains(err.Error(), resource) {
				t.Fatalf("post-commit error lacks phase/resource: %v", err)
			}
		})
	}
}

func TestCloseCaptureClientPreservesPrimaryFailure(t *testing.T) {
	primary := errors.New("capture failed")
	closeCause := errors.New("injected close failure")
	closer := &injectedCaptureCloser{err: closeCause}
	err := closeCaptureClient(primary, false, "radar client", closer)
	if closer.calls != 1 || !errors.Is(err, primary) || !errors.Is(err, closeCause) {
		t.Fatalf("joined close result = (%d calls, %v)", closer.calls, err)
	}
	if strings.Contains(err.Error(), "post-commit") {
		t.Fatalf("uncommitted output mislabeled as post-commit: %v", err)
	}
}

func TestCloseCaptureClientDoesNotRollbackCommittedOutput(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "capture.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.WriteAt([]byte("ADC"), 0); err != nil {
		t.Fatal(err)
	}
	if err := output.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	closeCause := errors.New("injected close failure")
	err = closeCaptureClient(nil, output.Committed(), "radar client", &injectedCaptureCloser{err: closeCause})
	if !errors.Is(err, closeCause) || !strings.Contains(err.Error(), "post-commit cleanup") {
		t.Fatalf("post-commit close error = %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(finalPath)
	if err != nil || string(data) != "ADC" {
		t.Fatalf("published output = %q, %v", data, err)
	}
}

type injectedCaptureCloser struct {
	calls int
	err   error
}

func (closer *injectedCaptureCloser) Close() error {
	closer.calls++
	return closer.err
}

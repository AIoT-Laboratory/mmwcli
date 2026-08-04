package capturefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitPublishesPart(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.bin")
	file, err := Create(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("adc")); err != nil {
		t.Fatal(err)
	}
	if err := file.Commit(); err != nil {
		t.Fatal(err)
	}
	if !file.Committed() {
		t.Fatal("Commit succeeded without marking the capture committed")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "adc" {
		t.Fatalf("output = %q", data)
	}
	if _, err := os.Stat(output + ".part"); !os.IsNotExist(err) {
		t.Fatalf("part file still exists: %v", err)
	}
}

func TestCloseKeepsPartAndRefusesOverwrite(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.bin")
	file, err := Create(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output + ".part"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(output); err == nil {
		t.Fatal("Create overwrote an existing part file")
	}
}

func TestCreateRejectsDanglingDestinationSymlink(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "capture.bin")
	missingTarget := filepath.Join(directory, "missing-target.bin")
	if err := os.Symlink(missingTarget, output); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	file, err := Create(output)
	if err == nil {
		_ = file.Close()
		t.Fatal("Create accepted an existing dangling destination symlink")
	}
	if _, lstatErr := os.Lstat(output); lstatErr != nil {
		t.Fatalf("destination symlink changed: %v", lstatErr)
	}
	if _, statErr := os.Stat(output + ".part"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Create made a part file before rejecting destination symlink: %v", statErr)
	}
}

func TestCommitNeverOverwritesDestinationThatAppearsLate(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.bin")
	file, err := Create(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("capture")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("user-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := file.Commit(); err == nil {
		t.Fatal("Commit overwrote a destination that appeared after Create")
	}
	if file.Committed() {
		t.Fatal("failed Commit marked the capture committed")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "user-data" {
		t.Fatalf("late destination was changed: %q", data)
	}
	if _, err := os.Stat(output + ".part"); err != nil {
		t.Fatalf("capture evidence was not retained: %v", err)
	}
}

func TestCommitContextCancellationKeepsPart(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.bin")
	file, err := Create(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("adc")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := file.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitContext error = %v, want context cancellation", err)
	}
	if file.Committed() {
		t.Fatal("canceled CommitContext marked the capture committed")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("canceled commit published final output: %v", err)
	}
	if data, err := os.ReadFile(output + ".part"); err != nil || string(data) != "adc" {
		t.Fatalf("canceled commit part = %q, %v", data, err)
	}
}

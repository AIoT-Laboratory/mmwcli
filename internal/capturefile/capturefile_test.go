package capturefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.bin")
	file, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !file.Committed() {
		t.Fatal("commit did not mark the file complete")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "adc" {
		t.Fatalf("adc = %q, %v", data, err)
	}
}

func TestCreateRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.bin")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path); err == nil {
		t.Fatal("Create replaced an existing file")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "existing" {
		t.Fatalf("existing adc = %q, %v", data, err)
	}
}

func TestCanceledCommitRetainsADC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.bin")
	file, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := file.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitContext error = %v, want context cancellation", err)
	}
	if file.Committed() {
		t.Fatal("canceled commit marked the file complete")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "adc" {
		t.Fatalf("retained adc = %q, %v", data, err)
	}
}

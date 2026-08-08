package capturefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransactionDirectoryPublishesWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	finalPath := filepath.Join(root, "session")
	directory, err := CreateTransactionDirectory(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory.PartPath(), "session.json"), []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := directory.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !directory.Committed() {
		t.Fatal("transaction directory did not report committed")
	}
	data, err := os.ReadFile(filepath.Join(finalPath, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete" {
		t.Fatalf("published data = %q", data)
	}
	if _, err := os.Stat(finalPath + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage remains after commit: %v", err)
	}
}

func TestTransactionDirectoryRetainsStageWhenDestinationAppears(t *testing.T) {
	root := t.TempDir()
	finalPath := filepath.Join(root, "session")
	directory, err := CreateTransactionDirectory(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory.PartPath(), "payload"), []byte("staged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(finalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalPath, "payload"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := directory.CommitContext(context.Background()); err == nil {
		t.Fatal("CommitContext replaced an existing destination")
	}
	data, err := os.ReadFile(filepath.Join(finalPath, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("existing data changed to %q", data)
	}
	if _, err := os.Stat(directory.PartPath()); err != nil {
		t.Fatalf("failed transaction did not retain stage: %v", err)
	}
}

func TestTransactionDirectoryCancellationKeepsStage(t *testing.T) {
	directory, err := CreateTransactionDirectory(filepath.Join(t.TempDir(), "session"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := directory.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitContext error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(directory.PartPath()); err != nil {
		t.Fatalf("cancelled transaction did not retain stage: %v", err)
	}
}

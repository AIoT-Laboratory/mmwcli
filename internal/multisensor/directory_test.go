package multisensor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type directoryGolden struct {
	SessionJSON string `json:"session_json"`
	Sources     []struct {
		SourceID   string `json:"source_id"`
		PayloadHex string `json:"payload_hex"`
		IndexHex   string `json:"index_hex"`
	} `json:"sources"`
}

func TestDirectoryPublishesGoldenSession(t *testing.T) {
	golden, session := loadDirectoryGolden(t)
	finalPath := filepath.Join(t.TempDir(), "multisensor-session")
	directory, err := CreateDirectory(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	writeDirectoryGolden(t, directory, golden)
	if err := directory.CommitContext(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if !directory.Committed() {
		t.Fatal("multi-sensor directory did not report committed")
	}
	actual, err := os.ReadFile(filepath.Join(finalPath, SessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != golden.SessionJSON {
		t.Fatal("published session.json differs from the cross-language golden")
	}
	artifact, err := directory.CommittedSessionArtifact()
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(golden.SessionJSON))
	if artifact.SizeBytes != uint64(len(golden.SessionJSON)) || artifact.SHA256 != wantDigest {
		t.Fatalf("session artifact = %+v, want size=%d sha=%x", artifact, len(golden.SessionJSON), wantDigest)
	}
}

func TestDirectoryRejectsCorruptArtifactAndRetainsStage(t *testing.T) {
	golden, session := loadDirectoryGolden(t)
	finalPath := filepath.Join(t.TempDir(), "multisensor-session")
	directory, err := CreateDirectory(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	writeDirectoryGolden(t, directory, golden)
	cameraPath, err := directory.SourcePath("camera-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cameraPath, "frames.bin"), []byte("corrupt-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := directory.CommitContext(context.Background(), session); err == nil {
		t.Fatal("corrupt source artifact was published")
	}
	if _, err := os.Stat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt transaction published final directory: %v", err)
	}
	if _, err := os.Stat(directory.PartPath()); err != nil {
		t.Fatalf("corrupt transaction did not retain stage: %v", err)
	}
}

func TestDirectoryRejectsUndeclaredSourceFile(t *testing.T) {
	golden, session := loadDirectoryGolden(t)
	directory, err := CreateDirectory(filepath.Join(t.TempDir(), "multisensor-session"))
	if err != nil {
		t.Fatal(err)
	}
	writeDirectoryGolden(t, directory, golden)
	radarPath, err := directory.SourcePath("radar-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(radarPath, "extra.bin"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := directory.CommitContext(context.Background(), session); err == nil {
		t.Fatal("undeclared source file was published")
	}
}

func loadDirectoryGolden(t *testing.T) (directoryGolden, Session) {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("testdata", "two-source-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden directoryGolden
	if err := json.Unmarshal(encoded, &golden); err != nil {
		t.Fatal(err)
	}
	session, err := UnmarshalSession([]byte(golden.SessionJSON))
	if err != nil {
		t.Fatal(err)
	}
	return golden, session
}

func writeDirectoryGolden(t *testing.T, directory *Directory, golden directoryGolden) {
	t.Helper()
	for _, source := range golden.Sources {
		path, err := directory.CreateSourceDirectory(source.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := hex.DecodeString(source.PayloadHex)
		if err != nil {
			t.Fatal(err)
		}
		index, err := hex.DecodeString(source.IndexHex)
		if err != nil {
			t.Fatal(err)
		}
		filename := "frames.bin"
		if source.SourceID == "radar-0" {
			filename = "adc.bin"
		}
		if err := os.WriteFile(filepath.Join(path, filename), payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, IndexFileName), index, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

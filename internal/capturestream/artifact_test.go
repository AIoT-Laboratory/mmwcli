package capturestream

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"

	"mmwcli/internal/capturefile"
)

func TestArtifactRequiresPublishedSessionDirectory(t *testing.T) {
	if _, err := ArtifactFromCommittedSession(nil); err == nil {
		t.Fatal("nil session directory produced capture stream evidence")
	}

	outputPath := filepath.Join(t.TempDir(), "capture")
	output, err := capturefile.CreateSessionDirectory(
		outputPath,
		func(ctx context.Context, stage capturefile.SessionStage) error {
			return stage.WriteFileContext(ctx, capturefile.SessionManifestFileName, []byte("{}"))
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ArtifactFromCommittedSession(output); err == nil {
		t.Fatal("staged session directory produced capture stream evidence")
	}

	payload := []byte{1, 2, 3, 4}
	if _, err := output.WriteAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	if err := output.Truncate(int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if err := output.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	artifact, err := ArtifactFromCommittedSession(output)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.sizeBytes != uint64(len(payload)) {
		t.Fatalf("artifact size = %d, want %d", artifact.sizeBytes, len(payload))
	}
	if want := sha256.Sum256(payload); artifact.sha256 != want {
		t.Fatalf("artifact digest = %x, want %x", artifact.sha256, want)
	}
}

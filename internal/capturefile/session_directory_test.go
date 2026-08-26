package capturefile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func testSessionFinalizer(ctx context.Context, stage SessionStage) error {
	adc := stage.ADC()
	if adc.SizeBytes != 3 {
		return fmt.Errorf("ADC size = %d, want 3", adc.SizeBytes)
	}
	if want := sha256.Sum256([]byte("adc")); adc.SHA256 != want {
		return fmt.Errorf("ADC SHA-256 = %x, want %x", adc.SHA256, want)
	}
	if err := stage.WriteFileContext(ctx, "radar.cfg", []byte("frameCfg 0 1 1 0 100 1 0\n")); err != nil {
		return err
	}
	return stage.WriteFileContext(ctx, SessionManifestFileName, []byte("{}\n"))
}

func TestSessionDirectoryCommitPublishesCompleteDirectory(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture-session")
	session, err := CreateSessionDirectory(output, testSessionFinalizer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final session visible before commit: %v", err)
	}
	if err := session.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !session.Committed() {
		t.Fatal("commit succeeded without marking the session committed")
	}
	if _, err := os.Stat(output + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session stage remains after commit: %v", err)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{SessionADCFileName, SessionManifestFileName, "radar.cfg"}
	if len(entries) != len(want) {
		t.Fatalf("session entries = %v, want %v", entryNames(entries), want)
	}
	for index, name := range want {
		if entries[index].Name() != name {
			t.Fatalf("session entries = %v, want %v", entryNames(entries), want)
		}
	}
	data, err := os.ReadFile(filepath.Join(output, SessionADCFileName))
	if err != nil || string(data) != "adc" {
		t.Fatalf("adc.bin = %q, %v", data, err)
	}
}

func TestSessionDirectoryExposesADCArtifactOnlyAfterPublication(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture")
	session, err := CreateSessionDirectory(output, testSessionFinalizer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.CommittedADCArtifact(); err == nil {
		t.Fatal("staged session exposed committed ADC evidence")
	}

	payload := []byte("adc")
	if _, err := session.WriteAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	if err := session.Truncate(int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if err := session.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	artifact, err := session.CommittedADCArtifact()
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SizeBytes != int64(len(payload)) {
		t.Fatalf("artifact size = %d, want %d", artifact.SizeBytes, len(payload))
	}
	wantDigest := sha256.Sum256(payload)
	if artifact.SHA256 != wantDigest {
		t.Fatalf("artifact SHA-256 = %x, want %x", artifact.SHA256, wantDigest)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestSessionDirectoryRetryReplacesStaleStage(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture-session")
	session, err := CreateSessionDirectory(output, testSessionFinalizer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.WriteAt([]byte("old"), 0); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output+".part", SessionADCFileName+".part"))
	if err != nil || string(data) != "old" {
		t.Fatalf("retained ADC part = %q, %v", data, err)
	}
	retry, err := CreateSessionDirectory(output, testSessionFinalizer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retry.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	if err := retry.CommitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(output, SessionADCFileName)); err != nil ||
		string(data) != "adc" {
		t.Fatalf("retried session ADC = %q, %v", data, err)
	}
}

func TestSessionDirectoryCommitCancellationRetainsADCPart(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture-session")
	session, err := CreateSessionDirectory(output, testSessionFinalizer)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitContext error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled commit published final output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output+".part", SessionADCFileName+".part")); err != nil {
		t.Fatalf("canceled commit did not retain ADC part: %v", err)
	}
}

func TestSessionDirectoryFinalizerFailureRetainsStage(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture-session")
	wantErr := errors.New("metadata failed")
	session, err := CreateSessionDirectory(output, func(ctx context.Context, stage SessionStage) error {
		if err := stage.WriteFileContext(ctx, "radar.cfg", []byte("profileCfg\n")); err != nil {
			return err
		}
		return wantErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	if err := session.CommitContext(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("CommitContext error = %v, want finalizer failure", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed finalizer published final output: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(output+".part", SessionADCFileName)); err != nil || string(data) != "adc" {
		t.Fatalf("retained ADC = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(output+".part", "radar.cfg")); err != nil || string(data) != "profileCfg\n" {
		t.Fatalf("retained metadata = %q, %v", data, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close error = %v", err)
	}
}

func TestSessionDirectoryCancellationAfterADCPublicationRetainsStage(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture-session")
	ctx, cancel := context.WithCancel(context.Background())
	session, err := CreateSessionDirectory(output, func(_ context.Context, stage SessionStage) error {
		if stage.ADC().SizeBytes != 3 {
			return fmt.Errorf("ADC size = %d, want 3", stage.ADC().SizeBytes)
		}
		cancel()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.WriteAt([]byte("adc"), 0); err != nil {
		t.Fatal(err)
	}
	if err := session.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitContext error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled commit published final output: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(output+".part", SessionADCFileName)); err != nil || string(data) != "adc" {
		t.Fatalf("retained completed ADC = %q, %v", data, err)
	}
}

func TestSessionDirectoryRequiresManifest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture-session")
	session, err := CreateSessionDirectory(output, func(context.Context, SessionStage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CommitContext(context.Background()); err == nil {
		t.Fatal("CommitContext published a session without capture.json")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete session was published: %v", err)
	}
}

func TestSessionDirectoryNeverOverwritesLateDestination(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, string)
		check  func(*testing.T, string)
	}{
		{
			name: "file",
			create: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("user-data"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "user-data" {
					t.Fatalf("late file = %q, %v", data, err)
				}
			},
		},
		{
			name: "empty-directory",
			create: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, path string) {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("late directory entries = %v, %v", entryNames(entries), err)
				}
			},
		},
		{
			name: "nonempty-directory",
			create: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "keep.txt"), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, path string) {
				data, err := os.ReadFile(filepath.Join(path, "keep.txt"))
				if err != nil || string(data) != "keep" {
					t.Fatalf("late directory file = %q, %v", data, err)
				}
			},
		},
		{
			name: "dangling-symlink",
			create: func(t *testing.T, path string) {
				if err := os.Symlink(path+"-missing", path); err != nil {
					t.Skipf("symbolic links unavailable: %v", err)
				}
			},
			check: func(t *testing.T, path string) {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("late symlink changed: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture-session")
			session, err := CreateSessionDirectory(output, testSessionFinalizer)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.WriteAt([]byte("adc"), 0); err != nil {
				t.Fatal(err)
			}
			test.create(t, output)
			if err := session.CommitContext(context.Background()); err == nil {
				t.Fatal("CommitContext overwrote a late destination")
			}
			if session.Committed() {
				t.Fatal("failed commit marked the session committed")
			}
			test.check(t, output)
			entries, err := os.ReadDir(output + ".part")
			if err != nil {
				t.Fatalf("failed commit did not retain session stage: %v", err)
			}
			want := []string{SessionADCFileName, SessionManifestFileName, "radar.cfg"}
			if got := entryNames(entries); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("retained session entries = %v, want %v", got, want)
			}
		})
	}
}

func TestCreateSessionDirectoryRejectsExistingPaths(t *testing.T) {
	for _, target := range []string{"final", "part"} {
		for _, kind := range []string{"file", "empty-directory", "nonempty-directory", "dangling-symlink"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				output := filepath.Join(t.TempDir(), "capture-session")
				occupied := output
				if target == "part" {
					occupied += ".part"
				}
				createOccupiedPath(t, occupied, kind)
				session, err := CreateSessionDirectory(output, testSessionFinalizer)
				if target == "part" {
					if err != nil {
						t.Fatal(err)
					}
					_ = session.Close()
					info, statErr := os.Lstat(output + ".part")
					if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
						t.Fatalf("replacement stage = %+v, %v", info, statErr)
					}
					return
				}
				if err == nil {
					_ = session.Close()
					t.Fatal("CreateSessionDirectory accepted an existing path")
				}
				assertOccupiedPathUnchanged(t, occupied, kind)
				if _, err := os.Lstat(output + ".part"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("CreateSessionDirectory made a stage before rejecting final: %v", err)
				}
			})
		}
	}
}

func createOccupiedPath(t *testing.T, path, kind string) {
	t.Helper()
	switch kind {
	case "file":
		if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "empty-directory":
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	case "nonempty-directory":
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "keep.txt"), []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "dangling-symlink":
		if err := os.Symlink(path+"-missing", path); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
	default:
		t.Fatalf("unknown occupied path kind %q", kind)
	}
}

func assertOccupiedPathUnchanged(t *testing.T, path, kind string) {
	t.Helper()
	switch kind {
	case "file":
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
			t.Fatalf("occupied file = %q, %v", data, err)
		}
	case "empty-directory":
		if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
			t.Fatalf("occupied directory entries = %v, %v", entryNames(entries), err)
		}
	case "nonempty-directory":
		if data, err := os.ReadFile(filepath.Join(path, "keep.txt")); err != nil || string(data) != "keep" {
			t.Fatalf("occupied directory file = %q, %v", data, err)
		}
	case "dangling-symlink":
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("occupied symlink changed: %v", err)
		}
	}
}

func TestSessionStageRejectsUnsafeMetadataNames(t *testing.T) {
	stage := SessionStage{directoryPath: t.TempDir()}
	for _, name := range []string{
		"", ".", "../capture.json", `sub\\capture.json`, "/capture.json",
		SessionADCFileName, "ADC.BIN", "extra.part", "trailing. ",
	} {
		if err := stage.WriteFileContext(context.Background(), name, nil); err == nil {
			t.Errorf("WriteFileContext accepted %q", name)
		}
	}
}

func TestIsUsableOutputRejectsTypedNilAndClosedOutputs(t *testing.T) {
	var nilFile *File
	var nilSession *SessionDirectory
	if IsUsableOutput(nil) || IsUsableOutput(nilFile) || IsUsableOutput(nilSession) {
		t.Fatal("IsUsableOutput accepted a nil output")
	}
	output, err := CreateSessionDirectory(
		filepath.Join(t.TempDir(), "capture-session"),
		testSessionFinalizer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !IsUsableOutput(output) {
		t.Fatal("IsUsableOutput rejected an open session output")
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if IsUsableOutput(output) {
		t.Fatal("IsUsableOutput accepted a closed session output")
	}
}

type cancelingReader struct {
	data   []byte
	cancel context.CancelFunc
	read   bool
}

func (r *cancelingReader) Read(destination []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	count := copy(destination, r.data)
	r.cancel()
	return count, nil
}

func TestHashSessionADCStopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{data: []byte("first block"), cancel: cancel}
	if _, err := hashSessionADC(ctx, reader, int64(len(reader.data))); !errors.Is(err, context.Canceled) {
		t.Fatalf("hashSessionADC error = %v, want context cancellation", err)
	}
}

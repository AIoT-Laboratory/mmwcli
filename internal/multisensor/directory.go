package multisensor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mmwcli/internal/capturefile"
)

const (
	SessionFileName            = "session.json"
	SensorsDirectoryName       = "sensors"
	IndexFileName              = "index.bin"
	MaximumDirectoryIndexBytes = uint64(64 << 20)
	directoryHashBufferBytes   = 1 << 20
)

// SessionArtifact is immutable evidence for session.json after the complete
// multi-sensor directory has been published.
type SessionArtifact struct {
	SizeBytes uint64
	SHA256    [sha256.Size]byte
}

// Directory owns one OUT.part aggregate transaction. SourcePath supports an
// existing transactional writer such as capturefile.SessionDirectory;
// CreateSourceDirectory supports an external producer recorder.
type Directory struct {
	transaction *capturefile.TransactionDirectory
	sensorsPath string
	artifact    SessionArtifact
}

func CreateDirectory(finalPath string) (*Directory, error) {
	transaction, err := capturefile.CreateTransactionDirectory(finalPath)
	if err != nil {
		return nil, err
	}
	sensorsPath := filepath.Join(transaction.PartPath(), SensorsDirectoryName)
	if err := os.Mkdir(sensorsPath, 0o755); err != nil {
		return nil, fmt.Errorf("create multi-sensor sources directory: %w", err)
	}
	return &Directory{transaction: transaction, sensorsPath: sensorsPath}, nil
}

func (directory *Directory) SourcePath(sourceID string) (string, error) {
	if directory == nil || directory.transaction == nil || directory.transaction.Committed() {
		return "", errors.New("multi-sensor directory is not open")
	}
	if len(sourceID) > MaximumSourceIDBytes || !sourceIDPattern.MatchString(sourceID) {
		return "", fmt.Errorf("source_id %q is not a safe lowercase directory leaf", sourceID)
	}
	return filepath.Join(directory.sensorsPath, sourceID), nil
}

func (directory *Directory) CreateSourceDirectory(sourceID string) (string, error) {
	path, err := directory.SourcePath(sourceID)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return "", fmt.Errorf("create multi-sensor source directory %q: %w", sourceID, err)
	}
	return path, nil
}

func (directory *Directory) FinalPath() string {
	if directory == nil || directory.transaction == nil {
		return ""
	}
	return directory.transaction.FinalPath()
}

func (directory *Directory) PartPath() string {
	if directory == nil || directory.transaction == nil {
		return ""
	}
	return directory.transaction.PartPath()
}

func (directory *Directory) Committed() bool {
	return directory != nil && directory.transaction != nil && directory.transaction.Committed()
}

func (directory *Directory) CommitContext(ctx context.Context, session Session) error {
	if directory == nil || directory.transaction == nil || directory.transaction.Committed() {
		return errors.New("multi-sensor directory is not open")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := MarshalSession(session)
	if err != nil {
		return err
	}
	indexes, err := directory.validateSources(ctx, session)
	if err != nil {
		return err
	}
	if err := session.ValidateWithIndexes(indexes); err != nil {
		return fmt.Errorf("validate multi-sensor session indices: %w", err)
	}
	if err := directory.validateSourceSet(session); err != nil {
		return err
	}
	artifact := SessionArtifact{SizeBytes: uint64(len(encoded)), SHA256: sha256.Sum256(encoded)}
	if err := writeSessionFile(ctx, filepath.Join(directory.PartPath(), SessionFileName), encoded); err != nil {
		return err
	}
	if err := validateAggregateRoot(directory.PartPath()); err != nil {
		return err
	}
	if err := directory.transaction.CommitContext(ctx); err != nil {
		return err
	}
	directory.artifact = artifact
	return nil
}

func (directory *Directory) CommittedSessionArtifact() (SessionArtifact, error) {
	if !directory.Committed() {
		return SessionArtifact{}, errors.New("multi-sensor directory is not committed")
	}
	return directory.artifact, nil
}

func (directory *Directory) validateSources(
	ctx context.Context,
	session Session,
) (map[string]SensorIndex, error) {
	indexes := make(map[string]SensorIndex)
	for _, source := range session.Sources {
		path, err := directory.SourcePath(source.SourceID)
		if err != nil {
			return nil, err
		}
		if source.Outcome != OutcomeComplete {
			if _, err := os.Lstat(path); err == nil {
				return nil, fmt.Errorf("non-complete source %q has a staged directory", source.SourceID)
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect source %q: %w", source.SourceID, err)
			}
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect complete source %q: %w", source.SourceID, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("complete source %q path is not a directory", source.SourceID)
		}
		index, err := validateSourceDirectory(ctx, path, source)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", source.SourceID, err)
		}
		indexes[source.SourceID] = index
	}
	return indexes, nil
}

func (directory *Directory) validateSourceSet(session Session) error {
	declared := make(map[string]SourceOutcome, len(session.Sources))
	for _, source := range session.Sources {
		declared[source.SourceID] = source.Outcome
	}
	entries, err := os.ReadDir(directory.sensorsPath)
	if err != nil {
		return fmt.Errorf("inspect multi-sensor sources: %w", err)
	}
	for _, entry := range entries {
		outcome, exists := declared[entry.Name()]
		if !exists {
			return fmt.Errorf("staged directory contains undeclared source %q", entry.Name())
		}
		if outcome != OutcomeComplete {
			return fmt.Errorf("staged directory contains non-complete source %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect source directory %q: %w", entry.Name(), err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source entry %q is not a directory", entry.Name())
		}
	}
	return nil
}

func validateSourceDirectory(ctx context.Context, path string, source Source) (SensorIndex, error) {
	declared := make(map[string]Artifact, len(source.Artifacts))
	for _, artifact := range source.Artifacts {
		declared[artifact.Path] = artifact
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return SensorIndex{}, fmt.Errorf("inspect source directory: %w", err)
	}
	if len(entries) != len(declared) {
		return SensorIndex{}, fmt.Errorf(
			"source directory has %d files, manifest declares %d",
			len(entries),
			len(declared),
		)
	}
	for _, entry := range entries {
		artifact, exists := declared[entry.Name()]
		if !exists {
			return SensorIndex{}, fmt.Errorf("source directory contains undeclared file %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return SensorIndex{}, fmt.Errorf("inspect artifact %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 ||
			uint64(info.Size()) != artifact.SizeBytes {
			return SensorIndex{}, fmt.Errorf("artifact %q is not the declared regular file", entry.Name())
		}
		digest, err := hashExactFile(ctx, filepath.Join(path, entry.Name()), artifact.SizeBytes)
		if err != nil {
			return SensorIndex{}, err
		}
		if hex.EncodeToString(digest[:]) != artifact.SHA256 {
			return SensorIndex{}, fmt.Errorf("artifact %q SHA-256 does not match session.json", entry.Name())
		}
	}
	indexArtifact, err := artifactByRole(source.Artifacts, ArtifactIndex)
	if err != nil {
		return SensorIndex{}, err
	}
	if indexArtifact.SizeBytes > MaximumDirectoryIndexBytes {
		return SensorIndex{}, fmt.Errorf(
			"index.bin is %d bytes, directory reader maximum is %d",
			indexArtifact.SizeBytes,
			MaximumDirectoryIndexBytes,
		)
	}
	encoded, err := os.ReadFile(filepath.Join(path, IndexFileName))
	if err != nil {
		return SensorIndex{}, fmt.Errorf("read index.bin: %w", err)
	}
	index, err := DecodeSensorIndex(encoded, source.Limits)
	if err != nil {
		return SensorIndex{}, fmt.Errorf("decode index.bin: %w", err)
	}
	return index, nil
}

func hashExactFile(ctx context.Context, path string, expected uint64) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return digest, fmt.Errorf("open artifact %s: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, directoryHashBufferBytes)
	var size uint64
	for {
		if err := ctx.Err(); err != nil {
			return digest, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if expected-size < uint64(count) {
				return digest, fmt.Errorf("artifact %s grew while hashing", path)
			}
			written, writeErr := hash.Write(buffer[:count])
			if writeErr != nil || written != count {
				return digest, errors.Join(writeErr, io.ErrShortWrite)
			}
			size += uint64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return digest, fmt.Errorf("hash artifact %s: %w", path, readErr)
		}
		if count == 0 {
			return digest, io.ErrNoProgress
		}
	}
	if size != expected {
		return digest, fmt.Errorf("artifact %s changed size while hashing: expected=%d actual=%d", path, expected, size)
	}
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func writeSessionFile(ctx context.Context, path string, encoded []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create multi-sensor session.json: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return err
	}
	written, writeErr := file.Write(encoded)
	if writeErr != nil || written != len(encoded) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return errors.Join(fmt.Errorf("write multi-sensor session.json: %w", writeErr), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync multi-sensor session.json: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close multi-sensor session.json: %w", err)
	}
	return nil
}

func validateAggregateRoot(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect multi-sensor aggregate stage: %w", err)
	}
	if len(entries) != 2 {
		return fmt.Errorf("multi-sensor aggregate stage has %d entries, want session.json and sensors", len(entries))
	}
	seenSession := false
	seenSensors := false
	for _, entry := range entries {
		switch entry.Name() {
		case SessionFileName:
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("multi-sensor session.json is not a regular file")
			}
			seenSession = true
		case SensorsDirectoryName:
			info, err := entry.Info()
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("multi-sensor sensors entry is not a directory")
			}
			seenSensors = true
		default:
			return fmt.Errorf("multi-sensor aggregate stage contains undeclared entry %q", entry.Name())
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".part") {
			return fmt.Errorf("multi-sensor aggregate stage contains incomplete entry %q", entry.Name())
		}
	}
	if !seenSession || !seenSensors {
		return errors.New("multi-sensor aggregate stage is incomplete")
	}
	return nil
}

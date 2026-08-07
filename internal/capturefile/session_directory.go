package capturefile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	SessionADCFileName      = "adc.bin"
	SessionManifestFileName = "capture.json"
)

// SessionStage exposes only the completed ADC artifact and an atomic helper
// for adding fixed-name metadata files before the outer directory is
// published.
type SessionStage struct {
	directoryPath string
	adc           SessionADCArtifact
}

// SessionADCArtifact is immutable evidence computed after the staged ADC file
// has been synced and closed.
type SessionADCArtifact struct {
	SizeBytes int64
	SHA256    [sha256.Size]byte
}

func (s SessionStage) ADC() SessionADCArtifact { return s.adc }

// WriteFileContext writes and syncs NAME.part, then publishes it as NAME
// without replacing an existing stage entry.
func (s SessionStage) WriteFileContext(ctx context.Context, name string, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateSessionLeafName(name); err != nil {
		return err
	}
	if strings.EqualFold(name, SessionADCFileName) ||
		strings.EqualFold(name, SessionADCFileName+".part") {
		return fmt.Errorf("session metadata name %q is reserved", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	finalPath := filepath.Join(s.directoryPath, name)
	partPath := finalPath + ".part"
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create session metadata part file %s: %w", partPath, err)
	}
	if n, writeErr := file.Write(data); writeErr != nil || n != len(data) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		closeErr := file.Close()
		return errors.Join(
			fmt.Errorf("write session metadata part file %s: %w", partPath, writeErr),
			wrapCloseError(partPath, closeErr),
		)
	}
	if err := file.Sync(); err != nil {
		closeErr := file.Close()
		return errors.Join(
			fmt.Errorf("sync session metadata part file %s: %w", partPath, err),
			wrapCloseError(partPath, closeErr),
		)
	}
	if err := ctx.Err(); err != nil {
		closeErr := file.Close()
		return errors.Join(err, wrapCloseError(partPath, closeErr))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close session metadata part file %s: %w", partPath, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishNoReplace(partPath, finalPath); err != nil {
		return fmt.Errorf("publish session metadata %s: %w", finalPath, err)
	}
	return nil
}

func validateSessionLeafName(name string) error {
	if name == "" || name == "." || filepath.IsAbs(name) || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\\`) || strings.HasSuffix(strings.ToLower(name), ".part") ||
		strings.TrimRight(name, ". ") != name {
		return fmt.Errorf("invalid session metadata file name %q", name)
	}
	return nil
}

func wrapCloseError(path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close session metadata part file %s: %w", path, err)
}

// SessionFinalizer adds the metadata files needed to interpret the completed
// ADC artifact. It runs after adc.bin has been synced and closed but before
// the session directory is published.
type SessionFinalizer func(context.Context, SessionStage) error

// SessionDirectory stages one capture under DEST.part and publishes the
// complete directory as DEST without overwrite. The output parent is assumed
// to be a cooperative namespace; this type does not defend against another
// process replacing the staging path itself while capture is in progress.
type SessionDirectory struct {
	finalPath string
	partPath  string
	adcPart   string
	adcFinal  string
	file      *os.File
	finalize  SessionFinalizer
	committed bool
}

func CreateSessionDirectory(finalPath string, finalize SessionFinalizer) (*SessionDirectory, error) {
	if finalPath == "" {
		return nil, errors.New("capture session output path is empty")
	}
	if finalize == nil {
		return nil, errors.New("capture session finalizer is nil")
	}
	abs, err := filepath.Abs(finalPath)
	if err != nil {
		return nil, fmt.Errorf("resolve capture session output: %w", err)
	}
	part := abs + ".part"
	if _, err := os.Lstat(abs); err == nil {
		return nil, fmt.Errorf("capture session output already exists: %s", abs)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("check capture session output %s: %w", abs, err)
	}
	if err := os.Mkdir(part, 0o755); err != nil {
		return nil, fmt.Errorf("create capture session part directory %s: %w", part, err)
	}
	adcPart := filepath.Join(part, SessionADCFileName+".part")
	file, err := os.OpenFile(adcPart, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		removeErr := os.Remove(part)
		return nil, errors.Join(
			fmt.Errorf("create capture session ADC part file %s: %w", adcPart, err),
			wrapRemoveStageError(part, removeErr),
		)
	}
	return &SessionDirectory{
		finalPath: abs,
		partPath:  part,
		adcPart:   adcPart,
		adcFinal:  filepath.Join(part, SessionADCFileName),
		file:      file,
		finalize:  finalize,
	}, nil
}

func wrapRemoveStageError(path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("remove incomplete capture session directory %s: %w", path, err)
}

func (s *SessionDirectory) WriteAt(data []byte, offset int64) (int, error) {
	if s == nil || s.file == nil {
		return 0, errors.New("capture session ADC part file is closed")
	}
	return s.file.WriteAt(data, offset)
}

func (s *SessionDirectory) Truncate(size int64) error {
	if s == nil || s.file == nil {
		return errors.New("capture session ADC part file is closed")
	}
	return s.file.Truncate(size)
}

func (s *SessionDirectory) CommitContext(ctx context.Context) error {
	if s == nil || s.file == nil {
		return errors.New("capture session ADC part file is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("sync capture session ADC part file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.file.Close(); err != nil {
		s.file = nil
		return fmt.Errorf("close capture session ADC part file: %w", err)
	}
	s.file = nil
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishNoReplace(s.adcPart, s.adcFinal); err != nil {
		return fmt.Errorf("publish capture session ADC: %w", err)
	}
	adc, err := inspectSessionADC(ctx, s.adcFinal)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage := SessionStage{directoryPath: s.partPath, adc: adc}
	if err := s.finalize(ctx, stage); err != nil {
		return fmt.Errorf("finalize capture session: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCompletedSessionStage(s.partPath, adc.SizeBytes); err != nil {
		return err
	}
	if err := publishDirectoryNoReplace(s.partPath, s.finalPath); err != nil {
		return fmt.Errorf("publish capture session %s: %w", s.finalPath, err)
	}
	s.committed = true
	return nil
}

func inspectSessionADC(ctx context.Context, path string) (artifact SessionADCArtifact, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return artifact, fmt.Errorf("open completed capture session ADC %s: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close completed capture session ADC %s: %w", path, err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return artifact, fmt.Errorf("inspect completed capture session ADC %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return artifact, fmt.Errorf("completed capture session ADC %s is not a regular file", path)
	}
	artifact, err = hashSessionADC(ctx, file, info.Size())
	if err != nil {
		return artifact, fmt.Errorf("hash completed capture session ADC %s: %w", path, err)
	}
	return artifact, nil
}

func hashSessionADC(ctx context.Context, reader io.Reader, expectedSize int64) (SessionADCArtifact, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return SessionADCArtifact{}, err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			written, writeErr := hash.Write(buffer[:count])
			if writeErr != nil {
				return SessionADCArtifact{}, writeErr
			}
			if written != count {
				return SessionADCArtifact{}, io.ErrShortWrite
			}
			size += int64(count)
		}
		if err := ctx.Err(); err != nil {
			return SessionADCArtifact{}, err
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return SessionADCArtifact{}, readErr
		}
		if count == 0 {
			return SessionADCArtifact{}, io.ErrNoProgress
		}
	}
	if size != expectedSize {
		return SessionADCArtifact{}, fmt.Errorf(
			"capture session ADC size changed while hashing: before=%d read=%d",
			expectedSize,
			size,
		)
	}
	artifact := SessionADCArtifact{SizeBytes: size}
	copy(artifact.SHA256[:], hash.Sum(nil))
	return artifact, nil
}

func validateCompletedSessionStage(partPath string, expectedADCSize int64) error {
	entries, err := os.ReadDir(partPath)
	if err != nil {
		return fmt.Errorf("inspect capture session stage %s: %w", partPath, err)
	}
	hasADC := false
	hasManifest := false
	for _, entry := range entries {
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".part") {
			return fmt.Errorf("capture session stage contains incomplete file %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect capture session entry %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("capture session entry %q is not a regular file", entry.Name())
		}
		switch entry.Name() {
		case SessionADCFileName:
			if info.Size() != expectedADCSize {
				return fmt.Errorf(
					"capture session ADC size changed after hashing: expected=%d actual=%d",
					expectedADCSize,
					info.Size(),
				)
			}
			hasADC = true
		case SessionManifestFileName:
			hasManifest = true
		}
	}
	if !hasADC {
		return errors.New("capture session stage is missing adc.bin")
	}
	if !hasManifest {
		return errors.New("capture session stage is missing capture.json")
	}
	return nil
}

func (s *SessionDirectory) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *SessionDirectory) FinalPath() string { return s.finalPath }
func (s *SessionDirectory) PartPath() string  { return s.partPath }
func (s *SessionDirectory) Committed() bool   { return s.committed }

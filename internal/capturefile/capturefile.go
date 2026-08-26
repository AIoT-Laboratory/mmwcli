package capturefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File keeps incomplete captures at OUT.part and publishes them only after Commit.
type File struct {
	finalPath string
	partPath  string
	file      *os.File
	committed bool
}

func Create(finalPath string) (*File, error) {
	if finalPath == "" {
		return nil, errors.New("capture output path is empty")
	}
	abs, err := filepath.Abs(finalPath)
	if err != nil {
		return nil, fmt.Errorf("resolve capture output: %w", err)
	}
	part := abs + ".part"
	if _, err := os.Lstat(abs); err == nil {
		return nil, fmt.Errorf("capture output already exists: %s", abs)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("check capture output %s: %w", abs, err)
	}
	if err := removeStalePart(abs, part); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create capture part file %s: %w", part, err)
	}
	return &File{finalPath: abs, partPath: part, file: file}, nil
}

func removeStalePart(finalPath, partPath string) error {
	expected := finalPath + ".part"
	if filepath.Clean(partPath) != filepath.Clean(expected) ||
		filepath.Dir(partPath) != filepath.Dir(finalPath) {
		return errors.New("capture part path does not match its destination")
	}
	if err := os.RemoveAll(partPath); err != nil {
		return fmt.Errorf("remove stale capture stage %s: %w", partPath, err)
	}
	return nil
}

func (f *File) Write(data []byte) (int, error) {
	if f == nil || f.file == nil {
		return 0, errors.New("capture part file is closed")
	}
	return f.file.Write(data)
}

func (f *File) WriteAt(data []byte, offset int64) (int, error) {
	if f == nil || f.file == nil {
		return 0, errors.New("capture part file is closed")
	}
	return f.file.WriteAt(data, offset)
}

func (f *File) Sync() error {
	if f == nil || f.file == nil {
		return errors.New("capture part file is closed")
	}
	return f.file.Sync()
}

func (f *File) Truncate(size int64) error {
	if f == nil || f.file == nil {
		return errors.New("capture part file is closed")
	}
	return f.file.Truncate(size)
}

func (f *File) Commit() error {
	return f.CommitContext(context.Background())
}

// CommitContext syncs and publishes the part file without overwriting an
// existing destination. Cancellation is checked before and after the blocking
// sync and again after close; the final check is the publication linearization
// point because the following same-volume publish is one atomic filesystem
// operation.
func (f *File) CommitContext(ctx context.Context) error {
	if f == nil || f.file == nil {
		return errors.New("capture part file is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.file.Sync(); err != nil {
		return fmt.Errorf("sync capture part file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.file.Close(); err != nil {
		f.file = nil
		return fmt.Errorf("close capture part file: %w", err)
	}
	f.file = nil
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishNoReplace(f.partPath, f.finalPath); err != nil {
		return fmt.Errorf("publish capture %s: %w", f.finalPath, err)
	}
	f.committed = true
	return nil
}

func (f *File) Close() error {
	if f == nil || f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *File) FinalPath() string { return f.finalPath }
func (f *File) PartPath() string  { return f.partPath }
func (f *File) Committed() bool   { return f.committed }

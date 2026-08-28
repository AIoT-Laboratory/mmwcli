package capturefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File writes one payload inside an already-staged take directory. Commit syncs
// and closes it; the enclosing directory transaction performs publication.
type File struct {
	path      string
	file      *os.File
	committed bool
}

func Create(path string) (*File, error) {
	if path == "" {
		return nil, errors.New("capture output path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve capture output: %w", err)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create capture file %s: %w", abs, err)
	}
	return &File{path: abs, file: file}, nil
}

func (f *File) WriteAt(data []byte, offset int64) (int, error) {
	if f == nil || f.file == nil {
		return 0, errors.New("capture file is closed")
	}
	return f.file.WriteAt(data, offset)
}

func (f *File) Truncate(size int64) error {
	if f == nil || f.file == nil {
		return errors.New("capture file is closed")
	}
	return f.file.Truncate(size)
}

// CommitContext syncs and closes the staged payload.
func (f *File) CommitContext(ctx context.Context) error {
	if f == nil || f.file == nil {
		return errors.New("capture file is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.file.Sync(); err != nil {
		return fmt.Errorf("sync capture file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.file.Close(); err != nil {
		f.file = nil
		return fmt.Errorf("close capture file: %w", err)
	}
	f.file = nil
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

func (f *File) Committed() bool { return f.committed }

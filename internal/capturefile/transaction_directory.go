package capturefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TransactionDirectory stages an arbitrary directory at DEST.part and
// publishes it as DEST without replacing an existing destination. Callers own
// all files below PartPath and must finish and validate them before Commit.
// Failed transactions deliberately retain the .part directory for diagnosis.
type TransactionDirectory struct {
	finalPath string
	partPath  string
	committed bool
}

func CreateTransactionDirectory(finalPath string) (*TransactionDirectory, error) {
	if finalPath == "" {
		return nil, errors.New("transaction directory output path is empty")
	}
	abs, err := filepath.Abs(finalPath)
	if err != nil {
		return nil, fmt.Errorf("resolve transaction directory output: %w", err)
	}
	part := abs + ".part"
	if _, err := os.Lstat(abs); err == nil {
		return nil, fmt.Errorf("transaction directory output already exists: %s", abs)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("check transaction directory output %s: %w", abs, err)
	}
	if err := os.Mkdir(part, 0o755); err != nil {
		return nil, fmt.Errorf("create transaction directory stage %s: %w", part, err)
	}
	return &TransactionDirectory{finalPath: abs, partPath: part}, nil
}

func (directory *TransactionDirectory) CommitContext(ctx context.Context) error {
	if directory == nil || directory.partPath == "" || directory.committed {
		return errors.New("transaction directory is not open")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(directory.partPath)
	if err != nil {
		return fmt.Errorf("inspect transaction directory stage %s: %w", directory.partPath, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("transaction directory stage %s is not a directory", directory.partPath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishDirectoryNoReplace(directory.partPath, directory.finalPath); err != nil {
		return fmt.Errorf("publish transaction directory %s: %w", directory.finalPath, err)
	}
	directory.committed = true
	return nil
}

func (directory *TransactionDirectory) FinalPath() string {
	if directory == nil {
		return ""
	}
	return directory.finalPath
}

func (directory *TransactionDirectory) PartPath() string {
	if directory == nil {
		return ""
	}
	return directory.partPath
}

func (directory *TransactionDirectory) Committed() bool {
	return directory != nil && directory.committed
}

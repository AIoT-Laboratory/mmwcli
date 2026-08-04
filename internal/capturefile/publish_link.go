//go:build !windows

package capturefile

import (
	"errors"
	"fmt"
	"os"
)

// publishNoReplace uses a same-directory hard link so publication fails if
// finalPath already exists. Removing partPath completes the move without ever
// exposing a partially written final file.
func publishNoReplace(partPath, finalPath string) error {
	if err := os.Link(partPath, finalPath); err != nil {
		return err
	}
	if err := os.Remove(partPath); err != nil {
		rollbackErr := os.Remove(finalPath)
		if rollbackErr == nil {
			return fmt.Errorf("remove capture part after publication: %w", err)
		}
		return errors.Join(
			fmt.Errorf("remove capture part after publication: %w", err),
			fmt.Errorf("rollback published capture: %w", rollbackErr),
		)
	}
	return nil
}

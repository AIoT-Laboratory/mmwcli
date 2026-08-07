//go:build !windows && !linux

package capturefile

import (
	"errors"
	"os"
)

func publishDirectoryNoReplace(partPath, finalPath string) error {
	return &os.LinkError{
		Op:  "publish-directory",
		Old: partPath,
		New: finalPath,
		Err: errors.ErrUnsupported,
	}
}

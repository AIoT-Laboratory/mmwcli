//go:build windows

package capturefile

import (
	"os"
	"strings"
	"syscall"
)

func publishDirectoryNoReplace(partPath, finalPath string) error {
	return moveNoReplace(partPath, finalPath)
}

func moveNoReplace(partPath, finalPath string) error {
	from, err := syscall.UTF16PtrFromString(windowsAPIPath(partPath))
	if err != nil {
		return &os.LinkError{Op: "move", Old: partPath, New: finalPath, Err: err}
	}
	to, err := syscall.UTF16PtrFromString(windowsAPIPath(finalPath))
	if err != nil {
		return &os.LinkError{Op: "move", Old: partPath, New: finalPath, Err: err}
	}
	if err := syscall.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "move", Old: partPath, New: finalPath, Err: err}
	}
	return nil
}

// os.OpenFile transparently uses the extended-length namespace for long
// Windows paths. Use the same form for the direct MoveFileW call so Commit
// does not reintroduce MAX_PATH after the part file was created successfully.
func windowsAPIPath(path string) string {
	if len(path) < 248 || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

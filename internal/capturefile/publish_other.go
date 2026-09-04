//go:build !windows && !linux

package capturefile

import "errors"

func publishDirectoryNoReplace(string, string) error {
	return errors.New("atomic capture publication is unsupported on this platform")
}

//go:build linux && !amd64

package capturefile

import "errors"

func publishDirectoryNoReplace(string, string) error {
	return errors.New("atomic capture publication currently requires Linux amd64")
}

//go:build !windows && !linux

package app

import "sync"

var hardwareMutex sync.Mutex

func acquireHardwareLock(string) (func(), error) {
	hardwareMutex.Lock()
	return hardwareMutex.Unlock, nil
}

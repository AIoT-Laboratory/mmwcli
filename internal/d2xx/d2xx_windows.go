//go:build windows && ftd2xx

package d2xx

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var procGetSystemDirectoryW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")

type windowsLibrary struct {
	path    string
	library *syscall.DLL
	version *syscall.Proc
}

func openNative() (nativeLibrary, error) {
	systemDirectory, err := windowsSystemDirectory()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(systemDirectory, "ftd2xx.dll")
	library, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("load FTDI D2XX library %s: %w", path, err)
	}
	version, err := library.FindProc("FT_GetLibraryVersion")
	if err != nil {
		_ = library.Release()
		return nil, fmt.Errorf("resolve FT_GetLibraryVersion in %s: %w", path, err)
	}
	return &windowsLibrary{path: path, library: library, version: version}, nil
}

func (library *windowsLibrary) info() (nativeInfo, error) {
	var rawVersion uint32
	result, _, _ := library.version.Call(uintptr(unsafe.Pointer(&rawVersion)))
	status := Status(uint32(result))
	if status != StatusOK {
		return nativeInfo{}, &StatusError{Operation: "FT_GetLibraryVersion", Status: status}
	}
	return nativeInfo{library: library.path, version: Version(rawVersion), versionKnown: true}, nil
}

func (library *windowsLibrary) close() error {
	if err := library.library.Release(); err != nil {
		return fmt.Errorf("release FTDI D2XX library %s: %w", library.path, err)
	}
	return nil
}

func windowsSystemDirectory() (string, error) {
	buffer := make([]uint16, syscall.MAX_PATH)
	for {
		length, _, callError := procGetSystemDirectoryW.Call(
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
		)
		if length == 0 {
			if callError != nil && callError != syscall.Errno(0) {
				return "", fmt.Errorf("query Windows system directory: %w", callError)
			}
			return "", fmt.Errorf("query Windows system directory")
		}
		if length < uintptr(len(buffer)) {
			return syscall.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, int(length)+1)
	}
}

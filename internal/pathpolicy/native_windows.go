package pathpolicy

import (
	"path/filepath"
	"strings"
	"syscall"
)

// WindowsPath converts a lexical path to a Win32 extended-length path.
func WindowsPath(path string) ([]uint16, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(path, "\\\\?\\") || strings.HasPrefix(path, "\\\\.\\"):
	case strings.HasPrefix(path, "\\\\"):
		path = "\\\\?\\UNC\\" + path[2:]
	default:
		path = "\\\\?\\" + path
	}
	return syscall.UTF16FromString(path)
}

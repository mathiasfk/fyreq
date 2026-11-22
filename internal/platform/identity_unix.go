//go:build darwin || linux

package platform

import (
	"fmt"
	"io/fs"
	"strconv"
	"syscall"
)

func nativeMetadata(_ string, info fs.FileInfo) (string, *string, error) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := strconv.FormatUint(uint64(stat.Uid), 10)
		return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), &uid, nil
	}
	return "", nil, fmt.Errorf("ownership metadata unavailable")
}

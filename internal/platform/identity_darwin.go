package platform

import (
	"fmt"
	"io/fs"
	"strconv"
	"syscall"
)

func identity(info fs.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	return ""
}

func ownershipID(info fs.FileInfo) (*string, error) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := strconv.FormatUint(uint64(stat.Uid), 10)
		return &uid, nil
	}
	return nil, fmt.Errorf("ownership metadata unavailable")
}

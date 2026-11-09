package platform

import (
	"fmt"
	"io/fs"
	"syscall"
)

func identity(info fs.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	return ""
}

func ownershipUID(info fs.FileInfo) *int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := int64(stat.Uid)
		return &uid
	}
	return nil
}

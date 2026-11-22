package platform

import (
	"context"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	volumeAPI        = syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives = volumeAPI.NewProc("GetLogicalDrives")
	getDriveType     = volumeAPI.NewProc("GetDriveTypeW")
)

type Roots struct{}

func (Roots) DefaultRoots(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	drives, _, err := getLogicalDrives.Call()
	if drives == 0 {
		return nil, fmt.Errorf("enumerate logical drives: %w", err)
	}
	var roots []string
	for letter := 'A'; letter <= 'Z'; letter++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if drives&(1<<(letter-'A')) == 0 {
			continue
		}
		root := string(letter) + ":\\"
		name := [4]uint16{uint16(letter), ':', '\\', 0}
		driveType, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(&name[0])))
		const driveFixed = 3
		if driveType == driveFixed {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no fixed drive roots found")
	}
	return roots, nil
}

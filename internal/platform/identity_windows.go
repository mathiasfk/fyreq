package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"unsafe"

	"fyreq/internal/pathpolicy"
)

var getSecurityInfo = syscall.NewLazyDLL("advapi32.dll").NewProc("GetSecurityInfo")

func nativeMetadata(path string, _ fs.FileInfo) (string, *string, error) {
	name, err := pathpolicy.WindowsPath(path)
	if err != nil {
		return "", nil, err
	}
	const readAttributes = 0x80
	const readControl = 0x20000
	const share = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE
	const flags = syscall.FILE_FLAG_BACKUP_SEMANTICS | syscall.FILE_FLAG_OPEN_REPARSE_POINT
	handle, ownerErr := syscall.CreateFile(&name[0], readAttributes|readControl, share, nil, syscall.OPEN_EXISTING, flags, 0)
	if errors.Is(ownerErr, syscall.ERROR_ACCESS_DENIED) {
		// Keep the identity when ACLs grant attribute access but deny owner access.
		handle, err = syscall.CreateFile(&name[0], readAttributes, share, nil, syscall.OPEN_EXISTING, flags, 0)
	} else {
		err = ownerErr
	}
	if err != nil {
		return "", nil, fmt.Errorf("open native metadata: %w", err)
	}
	defer syscall.CloseHandle(handle)

	var data syscall.ByHandleFileInformation
	var identity string
	if err = syscall.GetFileInformationByHandle(handle, &data); err == nil {
		index := uint64(data.FileIndexHigh)<<32 | uint64(data.FileIndexLow)
		identity = fmt.Sprintf("%d:%d", data.VolumeSerialNumber, index)
	} else {
		err = fmt.Errorf("file identity: %w", err)
	}
	var owner *string
	if ownerErr == nil {
		owner, ownerErr = windowsOwner(handle)
	}
	if ownerErr != nil {
		ownerErr = fmt.Errorf("filesystem owner: %w", ownerErr)
	}
	return identity, owner, errors.Join(err, ownerErr)
}

func windowsOwner(handle syscall.Handle) (*string, error) {
	const fileObject = 1
	const ownerSecurityInformation = 1
	var sid *syscall.SID
	var descriptor syscall.Handle
	status, _, _ := getSecurityInfo.Call(uintptr(handle), fileObject, ownerSecurityInformation,
		uintptr(unsafe.Pointer(&sid)), 0, 0, 0, uintptr(unsafe.Pointer(&descriptor)))
	if status != 0 {
		return nil, syscall.Errno(status)
	}
	defer syscall.LocalFree(descriptor)
	if sid == nil {
		return nil, fmt.Errorf("security descriptor has no owner SID")
	}
	id, err := sid.String()
	if err != nil {
		return nil, err
	}
	return &id, nil
}

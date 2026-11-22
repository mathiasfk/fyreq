package storage

import (
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"unsafe"

	"fyreq/internal/pathpolicy"
)

var setFileInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("SetFileInformationByHandle")

const (
	catalogShare = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE
	deleteAccess = 0x10000
)

func publicationSupported() error { return nil }

func openCatalogFile(path string) (*os.File, error) {
	name, err := pathpolicy.WindowsPath(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(&name[0], syscall.GENERIC_READ, catalogShare, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// Replace the manifest in one operation, preserving open readers of the old
// manifest. Both names are in the catalog directory on the same volume.
func publish(source, destination, _ string) (bool, error) {
	oldName, err := pathpolicy.WindowsPath(source)
	if err != nil {
		return false, err
	}
	newName, err := pathpolicy.WindowsPath(destination)
	if err != nil {
		return false, err
	}
	handle, err := syscall.CreateFile(&oldName[0], deleteAccess, catalogShare, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false, &os.PathError{Op: "open", Path: source, Err: err}
	}
	defer syscall.CloseHandle(handle)

	const fileRenameInfoEx = 22
	const replaceExisting = 1
	const posixSemantics = 2
	type renameInfo struct {
		flags          uint32
		rootDirectory  syscall.Handle
		fileNameLength uint32
		fileName       [1]uint16
	}
	var layout renameInfo
	length := len(newName) - 1
	offset := int(unsafe.Offsetof(layout.fileName))
	data := make([]byte, max(int(unsafe.Sizeof(layout)), offset+len(newName)*2))
	binary.LittleEndian.PutUint32(data, replaceExisting|posixSemantics)
	binary.LittleEndian.PutUint32(data[unsafe.Offsetof(layout.fileNameLength):], uint32(length*2))
	for i, unit := range newName[:length] {
		binary.LittleEndian.PutUint16(data[offset+i*2:], unit)
	}
	ok, _, err := setFileInformation.Call(uintptr(handle), fileRenameInfoEx, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	if ok == 0 {
		return false, &os.LinkError{Op: "replace manifest", Old: source, New: destination, Err: err}
	}
	return true, nil
}

// Segment and manifest contents are individually flushed before publication.
// Windows does not provide the POSIX directory fsync used by the Unix adapter.
func syncDirectory(string) error { return nil }

func removeStale(path string) error {
	name, err := pathpolicy.WindowsPath(path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(&name[0], deleteAccess, catalogShare, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)

	const fileDispositionInfoEx = 21
	const deleteFile = 1
	const posixSemantics = 2
	flags := uint32(deleteFile | posixSemantics)
	ok, _, err := setFileInformation.Call(uintptr(handle), fileDispositionInfoEx, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags))
	if ok == 0 {
		return err
	}
	return nil
}

// NTFS may deny new opens between marking a segment for deletion and closing
// the deleting handle. The caller rechecks the manifest before retrying.
func segmentRetiring(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(303)) // ERROR_DELETE_PENDING
}

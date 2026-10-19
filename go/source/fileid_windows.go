//go:build windows

package source

import (
	"encoding/hex"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileIDInfo is FILE_ID_INFO: the volume serial number and the 128-bit file
// identifier NTFS and ReFS keep for one file for its whole life.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// openShared opens path for reading without blocking another program's
// delete or rename (FILE_SHARE_DELETE): an ASUS updater replacing a model
// while it is hashed succeeds, and the changed identity drops the result.
// A reparse point is opened as itself, never followed.
func openShared(path string) (*os.File, error) {
	return openWith(path, windows.GENERIC_READ, windows.FILE_FLAG_SEQUENTIAL_SCAN)
}

// statID is the identity of the file at path, read through a handle opened
// for its attributes only: no content is read.
func statID(path string) (string, error) {
	f, err := openWith(path, windows.FILE_READ_ATTRIBUTES, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return fileID(f)
}

func openWith(path string, access, flags uint32) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// fileID is the volume serial number and FILE_ID_INFO identifier of the
// opened file, in hex.
func fileID(f *os.File) (string, error) {
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return "", err
	}
	return strconv.FormatUint(info.VolumeSerialNumber, 16) + ":" + hex.EncodeToString(info.FileID[:]), nil
}

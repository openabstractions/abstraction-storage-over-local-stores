//go:build unix

package source

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

// openShared opens path for reading without waiting on a FIFO and without
// following a link at its last component.
func openShared(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|openNonblock|syscall.O_NOFOLLOW, 0)
}

// statID is the identity of the file at path: its device and inode, read
// by lstat.
func statID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	return idOf(info)
}

// fileID is the device and inode of the opened file.
func fileID(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	return idOf(info)
}

func idOf(info os.FileInfo) (string, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("no device and inode")
	}
	return strconv.FormatUint(uint64(st.Dev), 16) + ":" + strconv.FormatUint(uint64(st.Ino), 16), nil
}

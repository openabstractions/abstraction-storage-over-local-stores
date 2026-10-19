package huggingface_test

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// makeSparse marks path sparse (FSCTL_SET_SPARSE) and extends it to size,
// so the extension occupies no disk.
func makeSparse(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var returned uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil); err != nil {
		t.Skipf("this volume makes no sparse file: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

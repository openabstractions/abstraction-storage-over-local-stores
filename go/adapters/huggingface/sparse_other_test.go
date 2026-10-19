//go:build !windows

package huggingface_test

import (
	"os"
	"testing"
)

// makeSparse extends path to size; the file systems these tests run on
// leave an extension by truncate unallocated.
func makeSparse(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

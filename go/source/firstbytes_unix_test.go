//go:build unix

package source

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO where a blob should be would block a plain open until a writer
// arrives, and the model bridge classifies while it holds its lock. The
// classifiers answer at once and treat it as no model format.
func TestClassifiersDoNotBlockOnAFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "sha256-"+"0123456789abcdef")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan [2]string, 1)
	go func() {
		pickle := "false"
		if IsPickle(fifo, "weights.bin") {
			pickle = "true"
		}
		done <- [2]string{ModelFormatOf(fifo, filepath.Base(fifo)), pickle}
	}()
	select {
	case got := <-done:
		if got[0] != "" || got[1] != "true" {
			t.Fatalf("a FIFO classified as format %q, pickle %s", got[0], got[1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("classifying a FIFO blocked")
	}
}

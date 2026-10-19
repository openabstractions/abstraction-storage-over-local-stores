//go:build !unix && !windows

package source

import (
	"fmt"
	"os"
	"runtime"
)

func openShared(path string) (*os.File, error) { return os.Open(path) }

// statID has no file identity to read on this platform; no digest is
// recorded here.
func statID(string) (string, error) {
	return "", fmt.Errorf("no file identity on %s", runtime.GOOS)
}

func fileID(*os.File) (string, error) {
	return "", fmt.Errorf("no file identity on %s", runtime.GOOS)
}

//go:build !windows

package source

// backgroundIO: only Windows' background processing mode is used; other
// platforms hash at the worker's ordinary priority.
func backgroundIO() func() { return func() {} }

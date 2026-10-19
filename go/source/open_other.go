//go:build !unix

package source

// openNonblock is zero where opening a path cannot wait on a FIFO writer:
// Windows names pipes outside the file system's paths.
const openNonblock = 0

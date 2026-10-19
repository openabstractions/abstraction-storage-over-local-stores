//go:build unix

package source

import "syscall"

// openNonblock keeps an open of a FIFO from waiting for a writer. A regular
// file ignores the flag.
const openNonblock = syscall.O_NONBLOCK

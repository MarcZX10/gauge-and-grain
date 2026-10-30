//go:build unix

package scan

import (
	"os"
	"syscall"
)

// openLog opens a log without waiting for a writer: a path swapped for a
// FIFO after it was stat'ed would otherwise block the worker forever. The
// caller checks the opened file is regular.
func openLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

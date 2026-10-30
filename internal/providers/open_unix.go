//go:build unix

package providers

import (
	"os"
	"syscall"
)

// openNoBlock opens a file without waiting for a writer, so a FIFO swapped in
// after it was stat'ed cannot hang the caller. The caller checks the type.
func openNoBlock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

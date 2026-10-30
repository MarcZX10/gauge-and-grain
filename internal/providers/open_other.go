//go:build !unix

package providers

import "os"

// openNoBlock opens a file. Windows has no FIFOs to block on.
func openNoBlock(path string) (*os.File, error) {
	return os.Open(path)
}

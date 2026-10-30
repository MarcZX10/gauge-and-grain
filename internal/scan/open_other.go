//go:build !unix

package scan

import "os"

// openLog opens a log. Windows has no FIFOs to block on.
func openLog(path string) (*os.File, error) {
	return os.Open(path)
}

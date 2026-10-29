//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on ANSI escape processing for the classic Windows console.
func enableVT() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		var mode uint32
		h := windows.Handle(f.Fd())
		if windows.GetConsoleMode(h, &mode) == nil {
			_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	}
}

//go:build unix

package scan

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// A named pipe in the log tree used to hang the whole program.
func TestNamedPipeInTheLogTreeDoesNotHang(t *testing.T) {
	cl, _ := env(t)
	writeClaude(t, filepath.Join(cl, "projects", "p", "ok.jsonl"), 1, 3)
	if err := syscall.Mkfifo(filepath.Join(cl, "projects", "p", "pipe.jsonl"), 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan *Result, 1)
	go func() {
		done <- Run(context.Background(), Options{Providers: []providers.Provider{providers.Claude{}}})
	}()
	select {
	case r := <-done:
		if len(r.Events) != 3 {
			t.Errorf("%d events", len(r.Events))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO hung the scan")
	}
	// even handed to the scanner directly (a file swapped in after listing)
	if _, _, err := processFile(context.Background(), job{providers.Claude{}, filepath.Join(cl, "projects", "p", "pipe.jsonl")}, "", false); err == nil {
		t.Error("processFile must refuse a FIFO")
	}
	_ = os.Remove(filepath.Join(cl, "projects", "p", "pipe.jsonl"))
}

// A path swapped for a FIFO after the stat must not hang the open: the type
// is checked on the opened file instead.
func TestOpeningAFIFODoesNotWait(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe.jsonl")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip(err)
	}
	type outcome struct {
		regular bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		f, err := openLog(p)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		done <- outcome{regular: err == nil && fi.Mode().IsRegular()}
	}()
	select {
	case o := <-done:
		if o.regular {
			t.Fatal("a FIFO was opened as a regular file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO waited for a writer")
	}
}

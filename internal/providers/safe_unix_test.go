//go:build unix

package providers

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe, or a link to a device, called x.jsonl must never be opened:
// reading it would block forever.
func TestWalkersIgnoreFIFOsAndDevices(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "a", "good.jsonl")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "a", "pipe.jsonl"), 0o644); err != nil {
		t.Skip("no mkfifo here:", err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(root, "a", "zero.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(good, filepath.Join(root, "a", "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	done := make(chan []string, 1)
	go func() { done <- walkJSONL([]string{root}) }()
	select {
	case got := <-done:
		m := map[string]bool{}
		for _, g := range got {
			m[filepath.Base(g)] = true
		}
		if !m["good.jsonl"] || !m["link.jsonl"] || m["pipe.jsonl"] || m["zero.jsonl"] || len(got) != 2 {
			t.Errorf("want good.jsonl and a link to a regular file only, got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walking hung")
	}
}

func TestReadRegularRefusesPipes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip(err)
	}
	if _, err := ReadRegular(p, 1<<20); err == nil {
		t.Error("a FIFO must be refused without opening it")
	}
}

// A data directory that is a symlink (to another disk) is followed.
func TestSymlinkedRootIsFollowed(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "s.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "projects")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := walkJSONL(existingDirs([]string{link})); len(got) != 1 {
		t.Errorf("a symlinked root must be followed: %v", got)
	}
}

// Opening a FIFO must not wait for a writer: ReadRegular checks the type of
// the opened file, which covers a FIFO that appears after its stat.
func TestOpenNoBlockDoesNotWaitOnAFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meta.json")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip("no mkfifo here:", err)
	}
	done := make(chan bool, 1)
	go func() {
		f, err := openNoBlock(p)
		if err != nil {
			done <- false
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		done <- err == nil && fi.Mode().IsRegular()
	}()
	select {
	case regular := <-done:
		if regular {
			t.Fatal("a FIFO was opened as a regular file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO waited for a writer")
	}
}

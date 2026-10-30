//go:build unix

package providers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A symlinked subdirectory is walked, also when it points outside the root,
// and a link back to an ancestor ends instead of looping.
func TestWalkFollowsSymlinkedDirectoriesAndSurvivesCycles(t *testing.T) {
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(elsewhere, "x"), 0o755)
	os.WriteFile(filepath.Join(elsewhere, "x", "far.jsonl"), []byte("{}\n"), 0o644)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.jsonl"), []byte("{}\n"), 0o644)
	if err := os.Symlink(filepath.Join(elsewhere, "x"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	done := make(chan []string, 1)
	go func() { done <- walkJSONL([]string{root}) }()
	select {
	case got := <-done:
		names := map[string]int{}
		for _, g := range got {
			names[filepath.Base(g)]++
		}
		if len(got) != 2 || names["a.jsonl"] != 1 || names["far.jsonl"] != 1 {
			t.Fatalf("want a.jsonl and linked/far.jsonl once each, got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a symlink cycle did not end the walk")
	}
}

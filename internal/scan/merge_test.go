package scan

import (
	"context"
	"fmt"
	"hash/maphash"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// slowFirst parses one file slowly, as a multi-gigabyte log would.
type slowFirst struct {
	providers.Claude
	slow string
}

func (s slowFirst) NewParser(path string, r *providers.Resume) providers.Parser {
	if path == s.slow {
		time.Sleep(300 * time.Millisecond)
	}
	return s.Claude.NewParser(path, r)
}

// While the first file is still parsing, the finished ones behind it must
// wait for a slot instead of piling up in memory.
func TestMergeBacklogIsBounded(t *testing.T) {
	cl, _ := env(t)
	for i := 0; i < 200; i++ {
		writeClaude(t, filepath.Join(cl, "projects", "p", fmt.Sprintf("f%03d.jsonl", i)), i, 2)
	}
	var maxSeen int
	mergeHook = func(n int) { maxSeen = max(maxSeen, n) }
	defer func() { mergeHook = nil }()
	p := slowFirst{providers.Claude{}, filepath.Join(cl, "projects", "p", "f000.jsonl")}
	r := Run(context.Background(), Options{Providers: []providers.Provider{p}})
	if len(r.Events) != 400 {
		t.Fatalf("%d events", len(r.Events))
	}
	if limit := 4 * max(2, runtime.NumCPU()); maxSeen > limit {
		t.Fatalf("%d finished files waited to merge (limit %d)", maxSeen, limit)
	}
}

// Two different calls whose keys hash alike must both survive; only the same
// key may be merged.
func TestHashCollisionDoesNotMergeDifferentCalls(t *testing.T) {
	old := keyHash
	keyHash = func(maphash.Seed, string) uint64 { return 7 } // every key collides
	defer func() { keyHash = old }()
	res := &Result{}
	m := newMerger(res)
	m.add(&entry{Events: []model.Event{{Key: "a", Input: 1}, {Key: "b", Input: 2}, {Key: "a", Input: 4}, {Key: "b", Input: 3}}})
	if len(res.Events) != 2 || res.Duplicate != 2 || res.Events[0].Input != 4 || res.Events[1].Input != 3 {
		t.Fatalf("collision merged different calls: %+v (duplicates %d)", res.Events, res.Duplicate)
	}
}

package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// After a parser upgrade the old cache still holds the usage of every log
// that did not change. AllowStale serves it at once; a later scan without it
// re-reads the logs and replaces the stale entries.
func TestStaleCacheServesUsageAtOnceAndRefreshesLater(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "a.jsonl")
	copyFixture(t, "claude/session-a.jsonl", f)
	old := time.Now().Add(-time.Hour) // settled: not a live log
	os.Chtimes(f, old, old)
	cache := t.TempDir()
	base := Options{Providers: []providers.Provider{versioned{providers.Claude{}, 1}}, CacheDir: cache}
	r1 := Run(context.Background(), base)
	want := total(r1.Events)

	// the parser is upgraded: the cache holds version 1, the code wants 2
	up := Options{Providers: []providers.Provider{versioned{providers.Claude{}, 2}}, CacheDir: cache, AllowStale: true}
	r2 := Run(context.Background(), up)
	if r2.Parsed != 0 || r2.Cached != 1 || r2.Stale != 1 || total(r2.Events) != want {
		t.Fatalf("stale cache must be served without re-reading: parsed=%d cached=%d stale=%d total=%d want %d",
			r2.Parsed, r2.Cached, r2.Stale, total(r2.Events), want)
	}
	// the stale entry is still there: it was not overwritten by serving it
	r3 := Run(context.Background(), up)
	if r3.Stale != 1 || r3.Parsed != 0 {
		t.Fatalf("serving stale data must not rewrite the cache: %+v", r3)
	}
	// a fresh scan replaces it
	up.AllowStale = false
	r4 := Run(context.Background(), up)
	if r4.Parsed != 1 || r4.Stale != 0 || total(r4.Events) != want {
		t.Fatalf("a fresh scan must re-read the stale file: parsed=%d stale=%d", r4.Parsed, r4.Stale)
	}
}

// A log that only grew while the cache is stale is served up to what the
// cache holds (its beginning is unchanged), and marked stale. A log that was
// rewritten is read again in full: its old usage is out of date.
func TestStaleCacheServesGrownLogsPrefixAndRereadsRewrittenOnes(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "a.jsonl")
	copyFixture(t, "claude/session-a.jsonl", f)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(f, old, old)
	cache := t.TempDir()
	r1 := Run(context.Background(), Options{Providers: []providers.Provider{versioned{providers.Claude{}, 1}}, CacheDir: cache})
	want := total(r1.Events)

	appendTo(t, f, `{"type":"assistant","sessionId":"s","requestId":"r9","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m9","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}`+"\n")
	up := Options{Providers: []providers.Provider{versioned{providers.Claude{}, 2}}, CacheDir: cache, AllowStale: true}
	r := Run(context.Background(), up)
	if r.Stale != 1 || r.Parsed != 0 || total(r.Events) != want {
		t.Fatalf("a grown log serves its cached prefix, stale: stale=%d parsed=%d total=%d want %d", r.Stale, r.Parsed, total(r.Events), want)
	}
	up.AllowStale = false
	if r := Run(context.Background(), up); r.Parsed != 1 || total(r.Events) != want+10 {
		t.Fatalf("the full scan includes the new record: parsed=%d total=%d", r.Parsed, total(r.Events))
	}
}

func TestStaleCacheRereadsARewrittenLog(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "a.jsonl")
	copyFixture(t, "claude/session-a.jsonl", f)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(f, old, old)
	cache := t.TempDir()
	Run(context.Background(), Options{Providers: []providers.Provider{versioned{providers.Claude{}, 1}}, CacheDir: cache})
	// rewritten, not appended: the beginning no longer matches the cache
	copyFixture(t, "claude/session-b-resumed.jsonl", f)
	r := Run(context.Background(), Options{Providers: []providers.Provider{versioned{providers.Claude{}, 2}}, CacheDir: cache, AllowStale: true})
	if r.Parsed != 1 || r.Stale != 0 {
		t.Fatalf("a rewritten log must be read again: parsed=%d stale=%d", r.Parsed, r.Stale)
	}
}

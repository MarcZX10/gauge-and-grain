package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// Text from a log reaches the terminal, so control characters must never
// survive ingestion: an ESC would let a log retitle the window or clear the
// screen.
func TestHostileStringsAreCleanedAndBounded(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "s.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	evil := `{"type":"assistant","sessionId":"s\u001b]0;PWNED\u0007","cwd":"/w/\u001b[2Jx","requestId":"r1","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m1","model":"claude\u001b[31m-opus","usage":{"input_tokens":5,"output_tokens":5}}}`
	huge := strings.Replace(evil, `"cwd":"/w/\u001b[2Jx"`, `"cwd":"/`+strings.Repeat("d", 5<<20)+`"`, 1)
	huge = strings.Replace(huge, `"m1"`, `"m2"`, 1)
	huge = strings.Replace(huge, `"r1"`, `"r2"`, 1)
	os.WriteFile(f, []byte(evil+"\n"+huge+"\n"), 0o644)
	cache := t.TempDir()
	for run := 0; run < 2; run++ { // fresh parse, then from the cache
		r := Run(context.Background(), Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache})
		if len(r.Events) != 2 {
			t.Fatalf("run %d: %d events", run, len(r.Events))
		}
		for _, e := range r.Events {
			for _, s := range []string{e.Model, e.Session, e.Project} {
				for _, c := range s {
					if c < 0x20 || (c >= 0x7f && c < 0xa0) {
						t.Fatalf("run %d: control character %q survived in %q", run, c, s)
					}
				}
				if len(s) > 1100 {
					t.Fatalf("run %d: %d-byte string kept", run, len(s))
				}
			}
		}
	}
	// the cache itself holds only the bounded text
	var biggest int64
	ents, _ := os.ReadDir(cache)
	for _, e := range ents {
		if fi, err := e.Info(); err == nil {
			biggest = max(biggest, fi.Size())
		}
	}
	if biggest > 64<<10 {
		t.Errorf("cache entry is %d bytes: oversized text must be bounded before it is cached", biggest)
	}
}

// Progress used to be called from every worker at once.
func TestProgressIsNeverConcurrent(t *testing.T) {
	cl, _ := env(t)
	for i := 0; i < 40; i++ {
		writeClaude(t, filepath.Join(cl, "projects", "p", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".jsonl"), i, 3)
	}
	var in, maxIn int
	var mu sync.Mutex
	var last int
	Run(context.Background(), Options{Providers: []providers.Provider{providers.Claude{}}, Progress: func(p Progress) {
		mu.Lock()
		in++
		maxIn = max(maxIn, in)
		mu.Unlock()
		time.Sleep(200 * time.Microsecond)
		last = p.Done // unsynchronised on purpose: -race flags a concurrent call
		mu.Lock()
		in--
		mu.Unlock()
	}})
	if maxIn != 1 || last != 40 {
		t.Errorf("progress callbacks overlapped (%d at once) or finished at %d/40", maxIn, last)
	}
}

// A last line without a newline is left for later while the file is young.
// If the file then stays unchanged for long enough, a cached result must not
// hide that line forever: the tail is read again without any write.
func TestUnterminatedLastLineIsCountedOnceTheFileIsIdle(t *testing.T) {
	old := idleAfter
	defer func() { idleAfter = old }()
	idleAfter = time.Hour
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "live.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	l := func(id string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"r` + id + `","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}`
	}
	os.WriteFile(f, []byte(l("1")+"\n"+l("2")), 0o644)
	mt := time.Now().Add(-30 * time.Minute)
	os.Chtimes(f, mt, mt)
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: t.TempDir()}
	if r := Run(context.Background(), o); total(r.Events) != 10 {
		t.Fatalf("young file: the open last line is not counted yet: %d", total(r.Events))
	}
	idleAfter = time.Minute // time passes; the file itself is not touched
	if r := Run(context.Background(), o); total(r.Events) != 20 || r.Cached != 0 {
		t.Fatalf("idle file: the open line must be read now: total=%d cached=%d", total(r.Events), r.Cached)
	}
	if r := Run(context.Background(), o); r.Cached != 1 || total(r.Events) != 20 {
		t.Fatalf("once complete it is cached again: %+v", r)
	}
}

// The model of a Cline task is read from a side file: writing it later must
// invalidate the cached result.
func TestSideFileIsPartOfTheCacheKey(t *testing.T) {
	user := t.TempDir()
	t.Setenv("GG_VSCODE_USER_DIRS", user)
	task := filepath.Join(user, "globalStorage", "saoudrizwan.claude-dev", "tasks", "1790000000000")
	os.MkdirAll(task, 0o755)
	doc := `[{"ts":1790000001000,"type":"say","say":"api_req_started","text":"{\"tokensIn\":10,\"tokensOut\":5}"}]`
	os.WriteFile(filepath.Join(task, "ui_messages.json"), []byte(doc), 0o644)
	o := Options{Providers: []providers.Provider{providers.ByID("cline")}, CacheDir: t.TempDir()}
	if r := Run(context.Background(), o); len(r.Events) != 1 || r.Events[0].Model != "unknown" {
		t.Fatalf("%+v", r.Events)
	}
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(filepath.Join(task, "task_metadata.json"), []byte(`{"model_usage":[{"ts":1,"model_id":"claude-sonnet-5-5"}]}`), 0o644)
	r := Run(context.Background(), o)
	if r.Parsed != 1 || r.Events[0].Model != "claude-sonnet-5-5" {
		t.Fatalf("the model written after the first scan must be picked up: parsed=%d %+v", r.Parsed, r.Events)
	}
}

func TestCacheIsPrivateAndPrunedAndClearIsScoped(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "s.jsonl")
	writeClaude(t, f, 1, 3)
	cache := filepath.Join(t.TempDir(), "cache")
	os.MkdirAll(cache, 0o755) // an older version made it world-readable
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache}
	Run(context.Background(), o)
	if fi, err := os.Stat(cache); err != nil || (fi.Mode().Perm()&0o077 != 0 && !isWindows()) {
		t.Errorf("cache dir must be owner-only: %v %v", fi.Mode(), err)
	}
	ents, _ := os.ReadDir(cache)
	if len(ents) != 1 {
		t.Fatalf("%d cache files", len(ents))
	}
	if fi, _ := ents[0].Info(); fi.Mode().Perm()&0o077 != 0 && !isWindows() {
		t.Errorf("cache file must be owner-only: %v", fi.Mode())
	}
	os.WriteFile(filepath.Join(cache, "notes.gob"), []byte("not ours"), 0o644)
	os.Remove(f) // the tool rotated the log away
	writeClaude(t, filepath.Join(cl, "projects", "p", "other.jsonl"), 2, 3)
	Run(context.Background(), o)
	ents, _ = os.ReadDir(cache)
	if len(ents) != 2 { // the entry for the new file + notes.gob; the orphan is gone
		var n []string
		for _, e := range ents {
			n = append(n, e.Name())
		}
		t.Errorf("orphan entry not pruned or foreign file touched: %v", n)
	}
	if _, err := ClearCache(cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "notes.gob")); err != nil {
		t.Error("cache clear must only remove files gg wrote")
	}
}

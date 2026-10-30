package scan

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
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

// A log tree on an unmounted drive shows up as a dead symlink: nothing is
// listed, which must not be read as "every log was removed".
func TestDeadRootKeepsItsCache(t *testing.T) {
	cl, _ := env(t)
	target := t.TempDir()
	writeClaude(t, filepath.Join(target, "p", "s.jsonl"), 1, 3)
	os.MkdirAll(cl, 0o755)
	if err := os.Symlink(target, filepath.Join(cl, "projects")); err != nil {
		t.Skip(err)
	}
	cache := t.TempDir()
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache}
	if r := Run(context.Background(), o); len(r.Events) != 3 {
		t.Fatalf("%d events", len(r.Events))
	}
	os.RemoveAll(target) // the drive is unplugged: the link is now dead
	Run(context.Background(), o)
	if ents, _ := os.ReadDir(cache); len(ents) != 1 {
		t.Fatalf("a dead root pruned the cache: %d entries left", len(ents))
	}
}

// GG_CACHE_DIR may be a directory the user owns: gg must not change its mode.
func TestExistingCacheDirKeepsItsMode(t *testing.T) {
	if isWindows() {
		t.Skip("POSIX modes")
	}
	cl, _ := env(t)
	writeClaude(t, filepath.Join(cl, "projects", "p", "s.jsonl"), 1, 3)
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	Run(context.Background(), Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: dir})
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("an existing directory's mode changed: %v %v", fi.Mode(), err)
	}
}

// A whole-document log above the limit is skipped by its size: its bytes are
// never read, so the scan allocates next to nothing for it.
func TestOversizedWholeFileIsSkippedUnread(t *testing.T) {
	user := t.TempDir()
	t.Setenv("GG_VSCODE_USER_DIRS", user)
	task := filepath.Join(user, "globalStorage", "saoudrizwan.claude-dev", "tasks", "1790000000000")
	os.MkdirAll(task, 0o755)
	f := filepath.Join(task, "ui_messages.json")
	os.WriteFile(f, nil, 0o644)
	if err := os.Truncate(f, maxWholeFile+1); err != nil { // sparse: no bytes are stored
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := Run(context.Background(), Options{Providers: []providers.Provider{providers.ByID("cline")}})
	runtime.ReadMemStats(&after)
	if r.Stats.Oversized != 1 {
		t.Fatalf("the oversized file must be counted: %+v", r.Stats)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("%d bytes allocated for a file that is skipped by size", grew)
	}
}

// An event dated in year 9999 would make the daily totals allocate millions
// of days: it is dropped and counted, while a plausible date is kept. The
// count survives the cache.
func TestImplausibleEventTimesAreDropped(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "s.jsonl")
	line := func(id, ts string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"r` + id + `","timestamp":"` + ts + `","message":{"id":"m` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}` + "\n"
	}
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(line("1", "9999-12-31T00:00:00Z")+line("2", "2015-06-01T00:00:00Z")), 0o644)
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: t.TempDir()}
	for run := 0; run < 2; run++ {
		r := Run(context.Background(), o)
		if len(r.Events) != 1 || r.Events[0].Time.Year() != 2015 || r.Skipped != 1 {
			t.Fatalf("run %d: %d events, skipped %d", run, len(r.Events), r.Skipped)
		}
	}
}

// sideTagged is Claude whose events take their model from a side file, as a
// Cline task takes it from its metadata. Only a fresh parse applies the tag.
type sideTagged struct {
	providers.Claude
	side string
}

func (s sideTagged) KeyFiles(string) []string { return []string{s.side} }

func (s sideTagged) NewParser(path string, r *providers.Resume) providers.Parser {
	b, _ := os.ReadFile(s.side)
	return tagged{s.Claude.NewParser(path, r), strings.TrimSpace(string(b)), r == nil}
}

type tagged struct {
	providers.Parser
	tag   string
	fresh bool
}

func (p tagged) Result() ([]model.Event, model.ParseStats) {
	evs, st := p.Parser.Result()
	if p.fresh {
		for i := range evs {
			evs[i].Model = p.tag
		}
	}
	return evs, st
}

// A key file changed while the log stayed the same size and time: the
// resumed state would keep the old model, so the log is parsed again.
func TestChangedKeyFileWithUnchangedLogIsReparsed(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "s.jsonl")
	writeClaude(t, f, 1, 2)
	side := filepath.Join(t.TempDir(), "model.txt")
	os.WriteFile(side, []byte("model-a"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(f, old, old)
	o := Options{Providers: []providers.Provider{sideTagged{providers.Claude{}, side}}, CacheDir: t.TempDir()}
	Run(context.Background(), o)
	os.WriteFile(side, []byte("model-b"), 0o644)
	later := time.Now().Add(-time.Minute)
	os.Chtimes(side, later, later)
	r := Run(context.Background(), o)
	if r.Parsed != 1 {
		t.Fatalf("the log must be parsed again: parsed=%d cached=%d", r.Parsed, r.Cached)
	}
	for _, e := range r.Events {
		if e.Model != "model-b" {
			t.Fatalf("event kept the old model %q", e.Model)
		}
	}
}

// A writer that is still on its last line may finish it after the file has
// been idle: the event must then be counted in full, not parsed from the rest.
func TestUnterminatedLineCompletedLaterIsCounted(t *testing.T) {
	old := idleAfter
	defer func() { idleAfter = old }()
	idleAfter = time.Minute
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "live.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	l := func(id string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"r` + id + `","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}`
	}
	half := l("2")[:len(l("2"))/2]
	os.WriteFile(f, []byte(l("1")+"\n"+half), 0o644)
	mt := time.Now().Add(-30 * time.Minute)
	os.Chtimes(f, mt, mt)
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: t.TempDir()}
	if r := Run(context.Background(), o); total(r.Events) != 10 {
		t.Fatalf("idle file: only the complete line counts yet: %d", total(r.Events))
	}
	appendTo(t, f, l("2")[len(half):]+"\n") // the writer finishes the line
	if r := Run(context.Background(), o); total(r.Events) != 20 {
		t.Fatalf("the completed line must be counted in full: %d", total(r.Events))
	}
}

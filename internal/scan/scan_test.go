package scan

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

func copyFixture(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", from))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// env points both providers at a throwaway home.
func env(t *testing.T) (claude, codex string) {
	root := t.TempDir()
	claude, codex = filepath.Join(root, "claude"), filepath.Join(root, "codex")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	return
}

func total(evs []model.Event) (n int64) {
	for i := range evs {
		n += evs[i].Total()
	}
	return
}

var both = []providers.Provider{providers.Claude{}, providers.Codex{}}

func TestFixturesEndToEndWithCrossFileAndArchiveDuplicates(t *testing.T) {
	cl, cx := env(t)
	copyFixture(t, "claude/session-a.jsonl", filepath.Join(cl, "projects", "-work-alpha", "a.jsonl"))
	copyFixture(t, "claude/session-b-resumed.jsonl", filepath.Join(cl, "projects", "-work-alpha", "b.jsonl"))
	copyFixture(t, "codex/records.jsonl", filepath.Join(cx, "sessions", "2026", "10", "02", "r.jsonl"))
	// the same session archived: must not be counted twice
	copyFixture(t, "codex/records.jsonl", filepath.Join(cx, "archived_sessions", "r.jsonl"))
	copyFixture(t, "codex/legacy.jsonl", filepath.Join(cx, "sessions", "2026", "09", "01", "l.jsonl"))

	res := Run(context.Background(), Options{Providers: both})
	var claude, codex int64
	for i := range res.Events {
		switch res.Events[i].Provider {
		case "claude":
			claude += res.Events[i].Total()
		case "codex":
			codex += res.Events[i].Total()
		}
	}
	if claude != 2477 {
		t.Errorf("claude total %d, want 2477 (resumed session's repeated message counted once)", claude)
	}
	if codex != 2885+395 {
		t.Errorf("codex total %d, want 3280 (archived copy ignored)", codex)
	}
	if res.Duplicate < 5 { // msg_1 in b + 4 archived records
		t.Errorf("duplicates merged = %d", res.Duplicate)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors: %v", res.Errors)
	}
	for _, e := range res.Events {
		if e.Key != "" {
			t.Fatal("keys must be released after de-duplication to save memory")
		}
	}
}

func TestCacheHitAndInvalidationOnChangeAndParserVersion(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "a.jsonl")
	copyFixture(t, "claude/session-a.jsonl", f)
	cache := t.TempDir()
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache}

	r1 := Run(context.Background(), o)
	if r1.Parsed != 1 || r1.Cached != 0 {
		t.Fatalf("first run: %+v", r1)
	}
	r2 := Run(context.Background(), o)
	if r2.Parsed != 0 || r2.Cached != 1 || total(r2.Events) != total(r1.Events) {
		t.Fatalf("second run must be served from cache with identical totals: parsed=%d cached=%d", r2.Parsed, r2.Cached)
	}

	// changed file -> re-read
	extra := `{"type":"assistant","sessionId":"s","requestId":"r9","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m9","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}` + "\n"
	appendTo(t, f, extra)
	r3 := Run(context.Background(), o)
	if r3.Parsed != 1 || total(r3.Events) != total(r1.Events)+10 {
		t.Fatalf("grown file: parsed=%d total=%d want %d", r3.Parsed, total(r3.Events), total(r1.Events)+10)
	}

	// parser version bump -> everything re-parsed even if the file is untouched
	o.Providers = []providers.Provider{versioned{providers.Claude{}, 99}}
	r4 := Run(context.Background(), o)
	if r4.Parsed != 1 || r4.Cached != 0 {
		t.Fatalf("parser version change must invalidate the cache: %+v", r4)
	}
}

type versioned struct {
	providers.Claude
	v int
}

func (v versioned) ParserVersion() int { return v.v }

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(s)
	fh.Close()
}

func TestAppendResumeMatchesFullReparse(t *testing.T) {
	cl, cx := env(t)
	_ = cl
	f := filepath.Join(cx, "sessions", "x", "r.jsonl")
	b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "codex", "records.jsonl"))
	lines := strings.SplitAfter(string(b), "\n")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte(strings.Join(lines[:6], "")), 0o644)
	cache := t.TempDir()
	o := Options{Providers: []providers.Provider{providers.Codex{}}, CacheDir: cache}
	Run(context.Background(), o)
	appendTo(t, f, strings.Join(lines[6:], ""))
	resumed := Run(context.Background(), o)
	full := Run(context.Background(), Options{Providers: []providers.Provider{providers.Codex{}}})
	if total(resumed.Events) != total(full.Events) || len(resumed.Events) != len(full.Events) {
		t.Fatalf("resume diverged from a full parse: %d/%d events, %d/%d tokens",
			len(resumed.Events), len(full.Events), total(resumed.Events), total(full.Events))
	}
	// rewrite with different early content: fingerprints must force a full re-parse
	os.WriteFile(f, []byte(strings.ReplaceAll(string(b), "resp_A", "resp_Z")), 0o644)
	again := Run(context.Background(), o)
	if total(again.Events) != total(full.Events) {
		t.Fatalf("rewritten file must be fully re-read: %d vs %d", total(again.Events), total(full.Events))
	}
}

func TestPartialTrailingLineIsNotCountedUntilComplete(t *testing.T) {
	cl, _ := env(t)
	f := filepath.Join(cl, "projects", "p", "live.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o755)
	full := `{"type":"assistant","sessionId":"s","requestId":"r1","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}`
	os.WriteFile(f, []byte(full+"\n"+full[:60]), 0o644) // writer is mid-line; mtime is "now"
	cache := t.TempDir()
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache}
	r := Run(context.Background(), o)
	if total(r.Events) != 10 || r.Stats.Malformed != 0 {
		t.Fatalf("live partial line must be ignored, not malformed: total=%d malformed=%d", total(r.Events), r.Stats.Malformed)
	}
	os.WriteFile(f, []byte(full+"\n"+strings.Replace(full, "m1", "m2", 1)+"\n"), 0o644)
	r = Run(context.Background(), o)
	if total(r.Events) != 20 {
		t.Fatalf("after completion: %d", total(r.Events))
	}
}

func TestCancelStopsPromptlyKeepsFinishedWorkAndResumes(t *testing.T) {
	cl, _ := env(t)
	for i := 0; i < 200; i++ {
		writeClaude(t, filepath.Join(cl, "projects", "p", fmt.Sprintf("f%03d.jsonl", i)), i, 50)
	}
	cache := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache, Progress: func(p Progress) {
		if n++; n == 20 {
			cancel()
		}
	}}
	r := Run(ctx, o)
	if !r.Cancelled || r.Parsed >= 200 || len(r.Errors) != 0 {
		t.Fatalf("cancel: cancelled=%v parsed=%d errors=%v", r.Cancelled, r.Parsed, r.Errors)
	}
	o.Progress = nil
	r2 := Run(context.Background(), Options{Providers: o.Providers, CacheDir: cache})
	if r2.Cancelled || r2.Cached == 0 || len(r2.Events) != 200*50 {
		t.Fatalf("resume after cancel: cached=%d events=%d", r2.Cached, len(r2.Events))
	}
}

func TestCorruptCacheEntryIsReparsedNotTrusted(t *testing.T) {
	cl, _ := env(t)
	copyFixture(t, "claude/session-a.jsonl", filepath.Join(cl, "projects", "p", "a.jsonl"))
	cache := t.TempDir()
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache}
	want := total(Run(context.Background(), o).Events)
	ents, _ := os.ReadDir(cache)
	for _, e := range ents {
		os.WriteFile(filepath.Join(cache, e.Name()), []byte("garbage"), 0o644)
	}
	r := Run(context.Background(), o)
	if r.Parsed != 1 || total(r.Events) != want {
		t.Fatalf("parsed=%d total=%d want %d", r.Parsed, total(r.Events), want)
	}
	if n, _ := ClearCache(cache); n == 0 {
		t.Error("ClearCache removed nothing")
	}
}

func TestUnreadableFileIsReportedAndOthersStillCount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	cl, _ := env(t)
	copyFixture(t, "claude/session-a.jsonl", filepath.Join(cl, "projects", "p", "a.jsonl"))
	bad := filepath.Join(cl, "projects", "p", "locked.jsonl")
	os.WriteFile(bad, []byte("x"), 0o000)
	r := Run(context.Background(), Options{Providers: []providers.Provider{providers.Claude{}}})
	if len(r.Errors) != 1 || len(r.Events) == 0 {
		t.Fatalf("errors=%v events=%d", r.Errors, len(r.Events))
	}
}

func writeClaude(t testing.TB, path string, file, calls int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var sb strings.Builder
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for c := 0; c < calls; c++ {
		ts := base.Add(time.Duration(file*calls+c) * time.Second).Format(time.RFC3339)
		line := fmt.Sprintf(`{"type":"assistant","sessionId":"s%d","cwd":"/w/p%d","requestId":"r%d_%d","timestamp":"%s","message":{"id":"m%d_%d","model":"claude-sonnet-5-5","usage":{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":4}}}`+"\n",
			file, file%7, file, c, ts, file, c)
		sb.WriteString(line)
		sb.WriteString(`{"type":"user","message":{"role":"user","content":"` + strings.Repeat("filler ", 40) + `"}}` + "\n")
		if c%10 == 0 { // streamed duplicate chunks + junk
			sb.WriteString(line)
			sb.WriteString(line)
			sb.WriteString(`{"type":"assistant","usage": broken "assistant"` + "\n")
		}
	}
	io.WriteString(fh, sb.String())
}

// TestLargeHistory builds ~1M records across 400 files with duplicates and
// garbage, then checks exact totals, that the cached rerun is fast, and
// that memory stays bounded.
func TestLargeHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("large history")
	}
	cl, _ := env(t)
	const files, calls = 400, 2500
	for i := 0; i < files; i++ {
		writeClaude(t, filepath.Join(cl, "projects", fmt.Sprintf("p%d", i%20), fmt.Sprintf("f%03d.jsonl", i)), i, calls)
	}
	cache := t.TempDir()
	o := Options{Providers: []providers.Provider{providers.Claude{}}, CacheDir: cache}

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()
	r := Run(context.Background(), o)
	cold := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&m1)

	want := int64(files * calls)
	if int64(len(r.Events)) != want || total(r.Events) != want*(1+2+3+4) {
		t.Fatalf("events=%d (want %d) tokens=%d", len(r.Events), want, total(r.Events))
	}
	wantBad := files * (calls / 10)
	if r.Stats.Malformed != wantBad {
		t.Errorf("malformed=%d want %d: garbage must be counted, not summed", r.Stats.Malformed, wantBad)
	}
	heapMB := (int64(m1.HeapAlloc) - int64(m0.HeapAlloc)) >> 20
	t.Logf("cold %v, retained heap +%d MB for %d events", cold, heapMB, len(r.Events))
	if heapMB > 400 {
		t.Errorf("retained heap %d MB for %d events is too much", heapMB, want)
	}
	start = time.Now()
	r2 := Run(context.Background(), o)
	warm := time.Since(start)
	t.Logf("warm %v", warm)
	if r2.Parsed != 0 || total(r2.Events) != total(r.Events) {
		t.Errorf("warm run parsed %d files", r2.Parsed)
	}
}

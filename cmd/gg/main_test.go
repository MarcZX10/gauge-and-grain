package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// gg builds the binary once and runs it against fixture homes, so the whole
// path (flags -> scan -> aggregate -> render) is exercised as a user sees it.
func gg(t *testing.T, bin, home string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+filepath.Join(home, "claude"), "CODEX_HOME="+filepath.Join(home, "codex"),
		"GEMINI_HOME="+filepath.Join(home, "gemini"), "QWEN_HOME="+filepath.Join(home, "qwen"), "PI_HOME="+filepath.Join(home, "pi"), "GG_VSCODE_USER_DIRS="+filepath.Join(home, "vscode", "User"), "OPENCODE_HOME="+filepath.Join(home, "opencode"), "KILO_HOME="+filepath.Join(home, "kilo"), "DSH_HOME="+filepath.Join(home, "dsh"),
		"GG_CACHE_DIR="+filepath.Join(home, "cache"), "GG_CONFIG_DIR="+filepath.Join(home, "cfg"), "NO_COLOR=1", "COLUMNS=100", "LANG=C.UTF-8")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func setup(t *testing.T) (bin, home string) {
	bin = filepath.Join(t.TempDir(), "gg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	home = t.TempDir()
	for from, to := range map[string]string{
		"claude/session-a.jsonl":         "claude/projects/p/a.jsonl",
		"claude/session-b-resumed.jsonl": "claude/projects/p/b.jsonl",
		"codex/records.jsonl":            "codex/sessions/2026/10/02/r.jsonl",
		"codex/legacy.jsonl":             "codex/sessions/2026/09/01/l.jsonl",
	} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", from))
		os.MkdirAll(filepath.Dir(filepath.Join(home, to)), 0o755)
		os.WriteFile(filepath.Join(home, to), b, 0o644)
	}
	return
}

func TestCLI(t *testing.T) {
	bin, home := setup(t)

	out, err := gg(t, bin, home, "report", "--all")
	if err != nil || strings.Contains(out, "\x1b") {
		t.Fatalf("plain report failed or coloured: %v\n%s", err, out)
	}
	for _, want := range []string{"Tokens", "Est. cost", "claude", "codex", "unpriced", "mystery-model-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}

	out, err = gg(t, bin, home, "export", "--all", "--format", "json", "--provider", "claude")
	if err != nil {
		t.Fatal(err, out)
	}
	var doc struct {
		Total struct {
			Tokens int64 `json:"total_tokens"`
			Calls  int   `json:"calls"`
		} `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("export is not clean JSON (progress/warnings must go to stderr only): %v\n%s", err, out)
	}
	if doc.Total.Tokens != 2477 || doc.Total.Calls != 5 {
		t.Errorf("claude total %+v, want 2477 tokens / 5 calls", doc.Total)
	}

	out, _ = gg(t, bin, home, "export", "--all", "--format", "csv", "--by", "model", "--provider", "codex")
	if !strings.Contains(out, "gpt-5.4,") || !strings.HasPrefix(out, "key,label,") {
		t.Errorf("csv:\n%s", out)
	}

	// date filters: the Sept legacy codex session is outside October
	out, _ = gg(t, bin, home, "export", "--from", "2026-10-01", "--to", "2026-10-31", "--format", "csv", "--by", "events", "--provider", "codex")
	if strings.Contains(out, "gpt-5,") && strings.Contains(out, "2026-09-01") {
		t.Errorf("date filter leaked September:\n%s", out)
	}

	for _, bad := range [][]string{{"report", "--provider", "nope"}, {"report", "--from", "2026-13-40"}, {"report", "--days", "0"}, {"frobnicate"}, {"export", "--format", "xml"}} {
		if out, err := gg(t, bin, home, bad...); err == nil || !strings.Contains(out, "gg:") {
			t.Errorf("%v should fail with a clear message, got err=%v out=%q", bad, err, out)
		}
	}

	out, _ = gg(t, bin, home, "notes", "--all")
	out = strings.Join(strings.Fields(out), " ") // ignore wrapping
	if !strings.Contains(out, "not your subscription bill") || !strings.Contains(out, "mystery-model-9") {
		t.Errorf("notes:\n%s", out)
	}
	out, _ = gg(t, bin, home, "report", "--all")
	out = strings.Join(strings.Fields(out), " ")
	if strings.Contains(out, "not your subscription bill. Plans") || !strings.Contains(out, "gg notes") {
		t.Errorf("default report must only point at the notes:\n%s", out)
	}

	out, _ = gg(t, bin, home, "report", "--all", "--compact")
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n > 6 || !strings.Contains(out, "tokens") || !strings.Contains(out, "sessions") || strings.Contains(out, "TOKEN MIX") {
		t.Errorf("--compact must print only the headline numbers (%d lines):\n%s", n, out)
	}
	if !strings.Contains(out, "attention") { // the fixtures contain an unpriced model
		t.Errorf("compact still points at notes that need attention:\n%s", out)
	}
	if o2, _ := gg(t, bin, home, "report", "-c", "--all"); o2 != out {
		t.Error("-c is an alias of --compact")
	}

	out, _ = gg(t, bin, home, "sources")
	if !strings.Contains(out, "Claude Code") || !strings.Contains(out, "Gemini CLI") {
		t.Errorf("sources:\n%s", out)
	}
	out, _ = gg(t, bin, home, "prices")
	if !strings.Contains(out, "anthropic") || !strings.Contains(out, "updated 2026-10-06") {
		t.Errorf("prices must show sources and dates:\n%s", out)
	}
	if out, err := gg(t, bin, home, "cache", "clear"); err != nil || !strings.Contains(out, "removed") {
		t.Errorf("cache clear: %v %s", err, out)
	}
}

func TestWatchIntervalIsOptIn(t *testing.T) {
	t.Setenv("GG_REFRESH", "")
	if got := watchInterval(false); got != 0 {
		t.Fatalf("automatic refresh must be off by default, got %s", got)
	}
	if got := watchInterval(true); got != 60*time.Second {
		t.Fatalf("watch default interval = %s, want 1m", got)
	}
	t.Setenv("GG_REFRESH", "15")
	if got := watchInterval(true); got != 15*time.Second {
		t.Fatalf("configured interval = %s, want 15s", got)
	}
	t.Setenv("GG_REFRESH", "0")
	if got := watchInterval(true); got != 0 {
		t.Fatalf("zero interval = %s, want disabled", got)
	}
}

func TestEmptyHomeExplainsInsteadOfFailing(t *testing.T) {
	bin, _ := setup(t)
	out, err := gg(t, bin, t.TempDir(), "report")
	if err != nil || !strings.Contains(out, "not found") || !strings.Contains(out, "Tokens") {
		t.Errorf("empty machine: %v\n%s", err, out)
	}
}

func TestOtherToolsEndToEnd(t *testing.T) {
	bin, home := setup(t)
	cp := func(from, to string) {
		err := filepath.WalkDir(filepath.Join("..", "..", "testdata", from), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join("..", "..", "testdata", from), p)
			b, _ := os.ReadFile(p)
			dst := filepath.Join(home, to, rel)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			return os.WriteFile(dst, b, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	cp("gemini", "gemini")
	cp("qwen", "qwen")
	cp("pi", "pi")
	cp("vscode", "vscode")
	cp("opencode", "opencode")

	out, err := gg(t, bin, home, "export", "--all", "--format", "json", "--provider", "gemini,qwen,pi,cline,roo,kilo,zoo,opencode")
	if err != nil {
		t.Fatal(err, out)
	}
	var doc struct {
		Total struct {
			Tokens int64 `json:"total_tokens"`
		} `json:"total"`
		ByProvider []struct {
			Key    string `json:"key"`
			Tokens int64  `json:"total_tokens"`
		} `json:"by_provider"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := map[string]int64{"gemini": 1680 + 365, "qwen": 1185, "pi": 5660, "cline": 660, "roo": 105, "kilo": 25, "zoo": 2, "opencode": 12}
	got := map[string]int64{}
	for _, p := range doc.ByProvider {
		got[p.Key] = p.Tokens
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %d tokens, want %d", k, got[k], v)
		}
	}

	out, _ = gg(t, bin, home, "notes", "--all")
	out = strings.Join(strings.Fields(out), " ")
	for _, who := range []string{"Gemini CLI is experimental", "Qwen Code is experimental", "Pi Agent is experimental", "Cline is experimental", "OpenCode is experimental"} {
		if !strings.Contains(out, who) {
			t.Errorf("notes must flag %q:\n%s", who, out)
		}
	}
	// a second run is served from the cache for whole-document files too
	out, _ = gg(t, bin, home, "report", "--all", "--provider", "cline", "--notes")
	if !strings.Contains(out, "claude-sonnet-5-5") {
		t.Errorf("cline report:\n%s", out)
	}
	if out, err := gg(t, bin, home, "sources"); err != nil || !strings.Contains(out, "Gemini CLI") || !strings.Contains(out, "experimental") {
		t.Errorf("sources should say experimental: %v\n%s", err, out)
	}
}

// --format and --by used where they do nothing are errors, not silent no-ops.
func TestFormatAndByRejectedWhereTheyDoNothing(t *testing.T) {
	bin, home := setup(t)
	for _, c := range []struct {
		args []string
		hint string
	}{
		{[]string{"report", "--format", "csv"}, "--format only applies to `gg export`"},
		{[]string{"export", "--by", "model"}, "--by only applies to `gg export --format csv`"},
		{[]string{"export", "--format", "json", "--by", "day"}, "--by only applies to `gg export --format csv`"},
	} {
		out, err := gg(t, bin, home, c.args...)
		if err == nil || !strings.Contains(out, c.hint) {
			t.Errorf("gg %v: want error with %q, got err=%v output:\n%s", c.args, c.hint, err, out)
		}
	}
	if out, err := gg(t, bin, home, "export", "--format", "csv", "--by", "model"); err != nil {
		t.Errorf("valid csv export failed: %v\n%s", err, out)
	}
}

// FORCE_COLOR=0 and =false switch colour off; any other value switches it on.
func TestForceColorValues(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	for v, want := range map[string]bool{"1": true, "yes": true, "0": false, "false": false} {
		t.Setenv("FORCE_COLOR", v)
		if got := theme(&opts{}, os.Stdout).Color; got != want {
			t.Errorf("FORCE_COLOR=%q: colour=%v, want %v", v, got, want)
		}
	}
}

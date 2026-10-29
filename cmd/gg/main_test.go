package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gg builds the binary once and runs it against fixture homes, so the whole
// path (flags -> scan -> aggregate -> render) is exercised as a user sees it.
func gg(t *testing.T, bin, home string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+filepath.Join(home, "claude"), "CODEX_HOME="+filepath.Join(home, "codex"),
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

	for _, bad := range [][]string{{"report", "--provider", "nope"}, {"report", "--from", "2026-13-40"}, {"report", "--days", "0"}, {"frobnicate"}, {"report", "--provider", "gemini"}, {"export", "--format", "xml"}} {
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

func TestEmptyHomeExplainsInsteadOfFailing(t *testing.T) {
	bin, _ := setup(t)
	out, err := gg(t, bin, t.TempDir(), "report")
	if err != nil || !strings.Contains(out, "not found") || !strings.Contains(out, "Tokens") {
		t.Errorf("empty machine: %v\n%s", err, out)
	}
}

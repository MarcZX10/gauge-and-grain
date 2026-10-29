package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Claude-Sonnet-5-5":         "claude-sonnet-5-5",
		"claude-opus-4-5-20251101":  "claude-opus-4-5",
		"claude-opus-4-5@20251101":  "claude-opus-4-5",
		"anthropic.claude-opus-5-5": "claude-opus-5-5",
		"openai/gpt-5.4":            "gpt-5.4",
		"claude-sonnet-5-5[1m]":     "claude-sonnet-5-5",
		"gpt-5-2025-08-07":          "gpt-5",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestLookupExactPrefixAndUnknown(t *testing.T) {
	tb, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if tb.Lookup("claude-sonnet-5-5") == nil || tb.Lookup("claude-sonnet-5-5-20261001") == nil {
		t.Error("exact and dated ids must be priced")
	}
	// longest match wins: sonnet-5-5 must not be priced as sonnet-5
	if p := tb.Lookup("claude-sonnet-5-5"); p.ID != "claude-sonnet-5-5" {
		t.Errorf("wrong match %s", p.ID)
	}
	for _, unknown := range []string{"mystery-model-9", "codex-auto-review", "gpt-5x", "claude-sonnet-50", "unknown", ""} {
		if tb.Lookup(unknown) != nil {
			t.Errorf("%q must be unpriced, not guessed", unknown)
		}
	}
}

func TestCostUsesCacheMultipliers(t *testing.T) {
	tb, _ := Load("")
	p := tb.Lookup("claude-sonnet-5-5") // $2 in, $10 out, $0.20 cache read
	e := &model.Event{Input: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, CacheWrite1h: 400_000, Output: 1_000_000}
	// 2 + 0.2 + 600k*2.5/1M + 400k*4/1M + 10
	want := 2 + 0.2 + 1.5 + 1.6 + 10
	if got := p.Cost(e); math.Abs(got-want) > 1e-9 {
		t.Errorf("cost=%v want %v", got, want)
	}
	o := tb.Lookup("gpt-5.4")
	if got := o.Cost(&model.Event{Input: 1e6, CacheRead: 1e6, Output: 1e6}); math.Abs(got-(2.5+0.25+15)) > 1e-9 {
		t.Errorf("openai cost=%v", got)
	}
}

func TestUserOverrideWinsAndBadFileIsReportedNotFatal(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "p.json")
	os.WriteFile(good, []byte(`{"models":[{"id":"codex-auto-review","input":1,"output":2},{"id":"gpt-5.4","input":9,"output":9}]}`), 0o644)
	tb, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if p := tb.Lookup("codex-auto-review"); p == nil || !p.User {
		t.Error("user price missing")
	}
	if p := tb.Lookup("gpt-5.4"); p.Input != 9 {
		t.Error("user override must win over the embedded price")
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{not json`), 0o644)
	tb, err = Load(bad)
	if err == nil || tb == nil || tb.Lookup("gpt-5.4") == nil {
		t.Error("bad override: expect a warning and the embedded table still usable")
	}
}

func TestEmbeddedTableIsSane(t *testing.T) {
	tb, _ := Load("")
	seen := map[string]bool{}
	for _, m := range tb.Models {
		if seen[m.ID] {
			t.Errorf("duplicate %s", m.ID)
		}
		seen[m.ID] = true
		if m.Input <= 0 || m.Output < m.Input || tb.SourceFor(&m) == nil {
			t.Errorf("%s: implausible or sourceless: %+v", m.ID, m)
		}
	}
	for _, s := range tb.Sources {
		if s.URL == "" || s.Updated == "" {
			t.Errorf("source %s must carry url and date", s.ID)
		}
	}
}

func TestGeminiModelsAreMatchedMostSpecificFirst(t *testing.T) {
	tb, _ := Load("")
	for in, want := range map[string]string{
		"gemini-2.5-flash":              "gemini-2.5-flash",
		"gemini-2.5-flash-lite":         "gemini-2.5-flash-lite",
		"gemini-2.5-flash-lite-preview": "gemini-2.5-flash-lite",
		"gemini-2.5-pro":                "gemini-2.5-pro",
	} {
		if p := tb.Lookup(in); p == nil || p.ID != want {
			t.Errorf("%s → %v, want %s", in, p, want)
		}
	}
	if tb.Lookup("qwen3-coder-plus") != nil {
		t.Error("Qwen prices are tiered by region and not listed for qwen3-coder-plus: it must stay unpriced")
	}
}

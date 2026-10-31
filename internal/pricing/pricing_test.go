package pricing

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"claude-sonnet-4.6":         "claude-sonnet-4-6",
		"claude-haiku-4.5":          "claude-haiku-4-5",
		"claude-opus-4.5@20251101":  "claude-opus-4-5",
		"gpt-4.1":                   "gpt-4.1",
		"gemini-2.5-flash":          "gemini-2.5-flash",
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
	p := tb.Lookup("claude-sonnet-5-5") // $2 in, $10 out, $0.10 cache read (0.05x, as Anthropic lists it)
	e := &model.Event{Input: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, CacheWrite1h: 400_000, Output: 1_000_000}
	// 2 + 0.1 + 600k*2.5/1M + 400k*4/1M + 10
	want := 2 + 0.1 + 1.5 + 1.6 + 10
	if got := p.Cost(e); math.Abs(got-want) > 1e-9 {
		t.Errorf("cost=%v want %v", got, want)
	}
	o := tb.Lookup("gpt-5.4")
	if got := o.Cost(&model.Event{Input: 1e6, CacheRead: 1e6, Output: 1e6}); math.Abs(got-(2.5+0.25+15)) > 1e-9 {
		t.Errorf("openai cost=%v", got)
	}
}

func TestUserOverrideWinsAndBadFileFails(t *testing.T) {
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
	// A named file that does not parse is an error, never a silent fallback.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{not json`), 0o644)
	if tb, err = Load(bad); err == nil || tb != nil {
		t.Error("a broken override must fail the load")
	}
	// Same for a named file that is missing.
	if tb, err = Load(filepath.Join(dir, "missing.json")); err == nil || tb != nil {
		t.Error("a missing override that was asked for must fail the load")
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

func TestLookupIsExactNotPrefix(t *testing.T) {
	// Unlisted variants must not inherit the parent's price.
	dir := t.TempDir()
	p := filepath.Join(dir, "p.json")
	os.WriteFile(p, []byte(`{"models":[{"id":"gpt-5","input":1,"output":10}]}`), 0o644)
	tb, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, unlisted := range []string{"gpt-5-nano", "gpt-5.1-codex-mini", "claude-opus-5-1"} {
		if tb.Lookup(unlisted) != nil {
			t.Errorf("%s must be unpriced, not costed at its parent", unlisted)
		}
	}
	if tb.Lookup("gpt-5") == nil {
		t.Error("exact id must still resolve")
	}
}

func TestAliasesResolveToTheirRow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "p.json")
	os.WriteFile(p, []byte(`{"models":[{"id":"gpt-5","input":1,"output":10,"aliases":["gpt-5-nano"]}]}`), 0o644)
	tb, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.Lookup("gpt-5-nano"); got == nil || got.ID != "gpt-5" {
		t.Errorf("alias must resolve to its row, got %+v", got)
	}
	if got := tb.Lookup("gpt-5-nano-2025-08-07"); got == nil || got.ID != "gpt-5" {
		t.Errorf("dated alias must resolve to its row, got %+v", got)
	}
}

func TestDottedClaudeVersionsResolve(t *testing.T) {
	tb, _ := Load("")
	for in, want := range map[string]string{
		"claude-sonnet-4.6": "claude-sonnet-4-6",
		"claude-haiku-4.5":  "claude-haiku-4-5",
	} {
		if p := tb.Lookup(in); p == nil || p.ID != want {
			t.Errorf("%s → %v, want %s", in, p, want)
		}
	}
	if tb.Lookup("gpt-4.1") != nil {
		t.Error("gpt-4.1 is not listed and must stay unpriced, not read as gpt-4-1")
	}
}

func TestOlderClaudeIdsArePriced(t *testing.T) {
	// Claude Code still writes these ids; prices from the Anthropic pricing page.
	tb, _ := Load("")
	for in, want := range map[string][2]float64{
		"claude-sonnet-4-5-20250929": {3, 15},
		"claude-opus-4-1-20250805":   {15, 75},
	} {
		p := tb.Lookup(in)
		if p == nil || p.Input != want[0] || p.Output != want[1] {
			t.Errorf("%s → %+v, want input %v output %v", in, p, want[0], want[1])
		}
	}
}

func TestCacheWriteDefaultsFollowProvider(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "p.json")
	os.WriteFile(p, []byte(`{"models":[
		{"id":"my-anthropic-alias","provider":"anthropic","input":2,"output":10},
		{"id":"claude-x","provider":"openai","input":2,"output":10}]}`), 0o644)
	tb, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	e := &model.Event{CacheWrite: 1_000_000}
	if got := tb.Lookup("my-anthropic-alias").Cost(e); math.Abs(got-2.5) > 1e-9 {
		t.Errorf("anthropic row with unusual id: cache write cost=%v want 2.5 (1.25x input)", got)
	}
	if got := tb.Lookup("claude-x").Cost(e); got != 0 {
		t.Errorf("openai row named claude-x: cache write cost=%v want 0", got)
	}
}

func TestLoadRejectsBadPrices(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct{ body, model, field string }{
		"negative input":      {`{"models":[{"id":"gpt-x","input":-1,"output":2}]}`, "gpt-x", "input"},
		"negative output":     {`{"models":[{"id":"gpt-x","input":1,"output":-2}]}`, "gpt-x", "output"},
		"negative cache read": {`{"models":[{"id":"gpt-x","input":1,"output":2,"cache_read":-0.1}]}`, "gpt-x", "cache_read"},
		"negative cache 5m":   {`{"models":[{"id":"gpt-x","input":1,"output":2,"cache_write_5m":-1}]}`, "gpt-x", "cache_write_5m"},
		"negative cache 1h":   {`{"models":[{"id":"gpt-x","input":1,"output":2,"cache_write_1h":-1}]}`, "gpt-x", "cache_write_1h"},
		"unknown currency":    {`{"currency":"EUR","models":[{"id":"gpt-x","input":1,"output":2}]}`, "", "currency"},
	}
	for name, c := range cases {
		f := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
		os.WriteFile(f, []byte(c.body), 0o644)
		_, err := Load(f)
		if err == nil {
			t.Errorf("%s: load must fail", name)
			continue
		}
		if !strings.Contains(err.Error(), c.field) || (c.model != "" && !strings.Contains(err.Error(), c.model)) {
			t.Errorf("%s: error %q must name the field %q and model %q", name, err, c.field, c.model)
		}
	}
}

func TestCheckPriceRejectsNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := checkPrice(&Price{ID: "gpt-x", Input: v, Output: 1}); err == nil {
			t.Errorf("input %v must be rejected", v)
		}
		if err := checkPrice(&Price{ID: "gpt-x", Input: 1, Output: v}); err == nil {
			t.Errorf("output %v must be rejected", v)
		}
	}
}

func TestStaleUsesOldestSourceAndInjectedTime(t *testing.T) {
	tb := &Table{Sources: []Source{{ID: "a", Updated: "2026-10-29"}, {ID: "b", Updated: "2026-10-06"}, {ID: "c", Updated: ""}}}
	oldest := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	if days, stale := tb.Stale(oldest.AddDate(0, 0, 90)); days != 90 || stale {
		t.Errorf("90 days old: days=%d stale=%v, want 90 false", days, stale)
	}
	if days, stale := tb.Stale(oldest.AddDate(0, 0, 91)); days != 91 || !stale {
		t.Errorf("91 days old: days=%d stale=%v, want 91 true", days, stale)
	}
	if days, _ := (&Table{}).Stale(oldest); days != 0 {
		t.Errorf("no dates: days=%d, want 0", days)
	}
	if got := StaleNote(120); got != "prices last updated 120 days ago; check docs/PRICING.md" {
		t.Errorf("StaleNote=%q", got)
	}
}

func TestNotesAreExposedOnPrices(t *testing.T) {
	// Price.Note is an exported field, read by the price list view.
	tb, _ := Load("")
	if p := tb.Lookup("claude-haiku-5-5"); p == nil || !strings.Contains(p.Note, "100K") {
		t.Errorf("haiku note missing: %+v", p)
	}
	if p := tb.Lookup("gemini-2.5-pro"); p == nil || !strings.Contains(p.Note, "200K") {
		t.Errorf("gemini pro note missing: %+v", p)
	}
}

// A row with no cache_read price (the "derived" rows of the table) prices
// cache reads at 0.1x input, and the table has no separate derived marker to
// fall out of step with that.
func TestRowWithoutCacheReadPriceDerivesTenthOfInput(t *testing.T) {
	pt, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	p := pt.Lookup("claude-sonnet-4-6")
	if p == nil || p.CacheRead != nil {
		t.Fatalf("want an embedded row without cache_read, got %+v", p)
	}
	e := model.Event{Model: "claude-sonnet-4-6", CacheRead: 1_000_000}
	if got := p.Cost(&e); math.Abs(got-0.3) > 1e-9 { // 0.1 x $3.00 per 1M
		t.Errorf("cache read cost = %v, want 0.3", got)
	}
}

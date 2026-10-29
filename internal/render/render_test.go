package render

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

func sample(t *testing.T) *Ctx {
	pt, _ := pricing.Load("")
	d := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	evs := []model.Event{
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "abcdef123456", Project: "/work/alpha", Time: d(1), Input: 100, CacheRead: 5000, CacheWrite: 300, Output: 200, Reasoning: 50, Has: model.HasCacheRead | model.HasCacheWrite | model.HasReasoning},
		{Provider: "codex", Model: "codex-auto-review", Session: "zzz", Project: "=cmd|' /C calc'!A0", Time: d(2), Input: 10, Output: 5},
		{Provider: "codex", Model: "gpt-5.4", Session: "yyy", Project: "/work/beta", Time: d(3), Input: 1000, Output: 100, CacheRead: 10, Has: model.HasCacheRead},
	}
	f := agg.Filter{From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	det := map[string]providers.Detection{
		"claude": {Status: providers.Ready}, "codex": {Status: providers.Ready},
		"gemini": {Status: providers.Unsupported, Detail: "found but not parsed"},
	}
	return &Ctx{Sum: agg.Summarize(evs, f, pt, time.UTC), Detect: det, Prices: pt, RangeName: "last 7 days",
		Stats: model.ParseStats{Malformed: 2}, Duplicate: 3}
}

func TestPlainReportHasNoEscapeCodesAndStaysWithinWidth(t *testing.T) {
	c := sample(t)
	for _, w := range []int{40, 60, 80, 120} {
		out := Report(Theme{ASCII: true}, c, w, ReportOpts{Daily: true, Notes: true})
		if strings.Contains(out, "\x1b") {
			t.Fatalf("w=%d: plain output contains escape codes", w)
		}
		for _, l := range strings.Split(out, "\n") {
			if Width(l) > min(w, 110) {
				t.Errorf("w=%d: line is %d cols: %q", w, Width(l), l)
			}
		}
	}
}

func TestReportStatesEstimatesGapsAndUnpriced(t *testing.T) {
	out := strings.Join(strings.Fields(Report(Theme{ASCII: true}, sample(t), 100, ReportOpts{Notes: true})), " ") // ignore wrapping
	for _, want := range []string{"not your subscription bill", "codex-auto-review", "Gemini CLI", "not parsed", "2 malformed", "duplicate", "partial"} {
		if !strings.Contains(out, want) {
			t.Errorf("report must mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$0.00") && strings.Contains(out, "codex-auto-review") && !strings.Contains(out, "unpriced") {
		t.Error("unpriced usage must not read as $0.00")
	}
}

func TestColourOnlyWhenEnabled(t *testing.T) {
	if !strings.Contains(Report(Theme{Color: true}, sample(t), 100, ReportOpts{}), "\x1b[") {
		t.Error("colour theme should colour")
	}
}

func TestJSONUsesNullForUnreportedAndUnpriced(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample(t)); err != nil {
		t.Fatal(err)
	}
	var d struct {
		ByModel []struct {
			Label      string   `json:"label"`
			CacheRead  *int64   `json:"cache_read_tokens"`
			CostUSD    *float64 `json:"cost_usd"`
			CostStatus string   `json:"cost_status"`
		} `json:"by_model"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(b.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	for _, m := range d.ByModel {
		if m.Label == "codex-auto-review" && (m.CostUSD != nil || m.CacheRead != nil || m.CostStatus != "unpriced") {
			t.Errorf("unpriced / unreported must be null, not zero: %+v", m)
		}
		if m.Label == "claude-sonnet-5-5" && (m.CostUSD == nil || m.CacheRead == nil || m.CostStatus != "priced") {
			t.Errorf("%+v", m)
		}
	}
	if len(d.Notes) == 0 {
		t.Error("notes missing")
	}
}

func TestCSVBlankCellsAndFormulaInjection(t *testing.T) {
	c := sample(t)
	var b bytes.Buffer
	if err := CSV(&b, c, "project", nil, &agg.Filter{}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows[1:] {
		if strings.ContainsAny(r[0][:1], "=+-@") {
			t.Errorf("formula injection not neutralised: %q", r[0])
		}
	}
	b.Reset()
	_ = CSV(&b, c, "model", nil, &agg.Filter{})
	rows, _ = csv.NewReader(&b).ReadAll()
	for _, r := range rows[1:] {
		if r[1] == "codex-auto-review" && (r[10] != "" || r[5] != "" || r[11] != "unpriced") {
			t.Errorf("unpriced model must export blank cost and blank unreported fields: %v", r)
		}
	}
	if err := CSV(&b, c, "nonsense", nil, &agg.Filter{}); err == nil {
		t.Error("unknown dimension must error")
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1500: "1.5K", 2_500_000: "2.50M", 3_200_000_000: "3.20B"} {
		if got := Tokens(in); got != want {
			t.Errorf("Tokens(%d)=%s want %s", in, got, want)
		}
	}
	if Num(1234567) != "1,234,567" || Cost(0.004) != "$0.0040" || Cost(1234.5) != "$1,235" {
		t.Error("num/cost formatting")
	}
	if Width(Theme{Color: true}.Warn("日本")) != 4 {
		t.Error("wide runes count double and colour codes count zero")
	}
}

func TestNotesAreSeparatedFromTheDefaultReportButAlwaysPointedTo(t *testing.T) {
	c := sample(t)
	def := strings.Join(strings.Fields(Report(Theme{ASCII: true}, c, 100, ReportOpts{})), " ")
	if strings.Contains(def, "not your subscription bill. Plans like") {
		t.Error("full notes must not be inlined in the default report")
	}
	if !strings.Contains(def, "attention") || !strings.Contains(def, "gg notes") {
		t.Errorf("default report must point at the notes (with a warning count):\n%s", def)
	}
	warn, all := NoteCounts(c)
	if warn < 2 || all <= warn { // unpriced model + unsupported Gemini; plus context notes
		t.Errorf("warn=%d all=%d", warn, all)
	}
	if !strings.Contains(strings.Join(strings.Fields(strings.Join(Notes(Theme{ASCII: true}, c, 100), " ")), " "), "not your subscription bill") {
		t.Error("Notes() must carry the cost caveat")
	}
}

func TestPanelAndSideKeepExactWidths(t *testing.T) {
	th := Theme{Color: true}
	p := th.Panel("Title that is far too long for this tiny panel", []string{"日本語 wide", th.Warn("coloured text that overflows"), "x"}, 20, CAccent)
	for _, l := range p {
		if Width(l) != 20 {
			t.Errorf("panel line is %d cols: %q", Width(l), Strip(l))
		}
	}
	s := th.Side([]int{20, 10}, 2, p, []string{"a"})
	for _, l := range s {
		if Width(l) != 32 {
			t.Errorf("side line is %d cols", Width(l))
		}
	}
	for _, l := range th.VChart([]float64{1, 5, 0, 9, 3}, 12, 4, 1, CAccent) {
		if Width(l) > 12 {
			t.Errorf("chart overflows: %d", Width(l))
		}
	}
}

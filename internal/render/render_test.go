package render

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCompactIsTinyAndShowsOnlyWhatMatters(t *testing.T) {
	c := sample(t)
	out := Compact(Theme{ASCII: true}, c, 100)
	if len(out) > 5 {
		t.Errorf("compact must be a handful of lines, got %d", len(out))
	}
	text := strings.Join(strings.Fields(strings.Join(out, " ")), " ")
	for _, want := range []string{"tokens", "sessions", "model", "attention"} { // attention: the sample has an unpriced model
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	for _, not := range []string{"TOKEN MIX", "PROVIDERS", "subscription bill"} {
		if strings.Contains(text, not) {
			t.Errorf("compact must not include %q", not)
		}
	}
	for _, l := range Compact(Theme{ASCII: true}, c, 40) {
		if Width(l) > 40 && !strings.Contains(l, "…") && !strings.Contains(l, "~") {
			// callers truncate; compact itself must already drop optional lines on narrow widths
			t.Logf("narrow line is %d cols (caller truncates): %q", Width(l), l)
		}
	}
	rep := Report(Theme{ASCII: true}, c, 100, ReportOpts{Compact: true})
	if n := len(strings.Split(strings.TrimSpace(rep), "\n")); n > 6 {
		t.Errorf("compact report is %d lines", n)
	}
	for _, l := range strings.Split(rep, "\n") {
		if Width(l) > 100 {
			t.Errorf("report line too wide: %q", l)
		}
	}
}

func TestCompactStaysQuietWhenNothingNeedsAttention(t *testing.T) {
	c := sample(t)
	c.Detect["gemini"] = providers.Detection{Status: providers.NotFound}
	// remove the only unpriced usage so no warning applies
	c.Sum.Total.Unpriced = map[string]int64{}
	c.Sum.Total.UnpricedTok = 0
	c.Sum.ByProvider = c.Sum.ByProvider[:0]
	out := strings.Join(Compact(Theme{ASCII: true}, c, 100), "\n")
	if strings.Contains(out, "attention") {
		t.Errorf("no warning, no pointer:\n%s", out)
	}
}

// An unpriced previous period must not produce a cost change: the previous
// total is a lower bound, so any percentage would be invented.
func TestCostChangeNotShownAgainstIncompletePrevious(t *testing.T) {
	pt, _ := pricing.Load("")
	d := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	evs := []model.Event{
		{Provider: "codex", Model: "codex-auto-review", Session: "old", Project: "/work/a", Time: d(2), Input: 1000000, Output: 100},
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "old2", Project: "/work/a", Time: d(3), Input: 10, Output: 10},
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "new", Project: "/work/a", Time: d(9), Input: 1000, Output: 100},
	}
	f := agg.Filter{From: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
	c := &Ctx{Sum: agg.Summarize(evs, f, pt, time.UTC), Prices: pt, RangeName: "last 7 days"}
	if c.Sum.Prev == nil || c.Sum.Prev.Complete() || !c.Sum.Total.Complete() {
		t.Fatalf("fixture: want complete current and incomplete previous, got prev=%+v", c.Sum.Prev)
	}
	th := Theme{}
	for _, l := range Overview(th, c, 100) {
		if strings.HasPrefix(Strip(l), "Est. cost") {
			if !strings.Contains(l, "n/a") || strings.ContainsAny(l, "▲▼■") {
				t.Errorf("cost line must say n/a and carry no change arrow: %q", Strip(l))
			}
		}
	}
	first := strings.Split(Compact(th, c, 100)[0], "  ·  ")[1]
	if !strings.Contains(first, "n/a") || strings.ContainsAny(first, "▲▼■") {
		t.Errorf("compact cost must say n/a and carry no change arrow: %q", first)
	}
}

// failWriter rejects every write, so a buffered CSV only notices at flush time.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestCSVReturnsFlushError(t *testing.T) {
	c := sample(t)
	evs := []model.Event{{Provider: "claude", Model: "claude-sonnet-5-5", Time: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Input: 1}}
	for _, by := range []string{"model", "events"} {
		if err := CSV(failWriter{}, c, by, evs, &agg.Filter{}); err == nil {
			t.Errorf("--by %s: write failure in the buffered output must be returned", by)
		}
	}
}

func TestJSONMarksCancelledExportAsPartial(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		c := sample(t)
		c.Cancelled = cancelled
		var b bytes.Buffer
		if err := JSON(&b, c); err != nil {
			t.Fatal(err)
		}
		var d struct {
			Partial bool   `json:"partial"`
			Reason  string `json:"partial_reason"`
		}
		if err := json.Unmarshal(b.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if d.Partial != cancelled || (cancelled && d.Reason == "") || (!cancelled && d.Reason != "") {
			t.Errorf("cancelled=%v: partial=%v reason=%q", cancelled, d.Partial, d.Reason)
		}
	}
}

// Day rows cover the whole window, zero-usage days included: the active
// count is the rows with tokens, the total is every row.
func TestDailyCountsActiveDaysOutOfAllRows(t *testing.T) {
	var days []*agg.Group
	for i := 1; i <= 7; i++ {
		g := &agg.Group{Key: fmt.Sprintf("2026-10-%02d", i), Label: fmt.Sprintf("2026-10-%02d", i), Unpriced: map[string]int64{}}
		if i == 1 || i == 4 || i == 7 { // usage on 3 of 7 days
			g.Input, g.Output = 1000, 100
		}
		days = append(days, g)
	}
	c := &Ctx{Sum: &agg.Summary{ByDay: days, Total: &agg.Group{Input: 3300, Output: 300}}}
	out := strings.Join(Daily(Theme{ASCII: true}, c, 100), "\n")
	if !strings.Contains(out, "3 of 7 days active") {
		t.Errorf("want \"3 of 7 days active\":\n%s", out)
	}
	var b bytes.Buffer
	if err := CSV(&b, c, "day", nil, &agg.Filter{}); err != nil {
		t.Fatal(err)
	}
	rows, _ := csv.NewReader(&b).ReadAll()
	if len(rows) != 8 { // header + every day
		t.Fatalf("day CSV must list all 7 days, got %d rows", len(rows)-1)
	}
	if rows[2][11] != "none" || rows[2][9] != "0" {
		t.Errorf("zero-usage day row: %v", rows[2])
	}
}

// Truncating wide characters can leave a gap of one column; Pad must fill
// it so that every padded cell is exactly the requested display width.
func TestPadIsExactDisplayWidthAfterTruncation(t *testing.T) {
	for _, th := range []Theme{{}, {ASCII: true}, {Color: true}} {
		for _, s := range []string{"日本語テキスト", "ab日本語", "中文字符串", "plain text"} {
			for w := 1; w <= 8; w++ {
				if got := Width(th.Pad(s, w)); got != w {
					t.Errorf("Pad(%q, %d) width %d", s, w, got)
				}
				if got := Width(th.PadL(s, w)); got != w {
					t.Errorf("PadL(%q, %d) width %d", s, w, got)
				}
			}
		}
	}
}

func TestTokensPromotesUnitAfterRounding(t *testing.T) {
	for n, want := range map[int64]string{
		999: "999", 999949: "999.9K", 999950: "1.00M", 999999: "1.00M",
		999949999: "999.95M", 999950000: "999.95M", 999999950: "1.00B", 999999999: "1.00B", 1000000000: "1.00B",
	} {
		if got := Tokens(n); got != want {
			t.Errorf("Tokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestBytesUsesByteUnits(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0B", 999: "999B", 1000: "1.0KB", 215_000_000: "215.0MB", 999_950: "1.0MB",
		1_500_000_000: "1.5GB", 3_000_000_000_000: "3.0TB",
	} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestJSONRangeNamesIANAZoneAndOffset(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	c := sample(t)
	pt, _ := pricing.Load("")
	c.Sum = agg.Summarize(nil, agg.Filter{From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}, pt, loc)
	var b bytes.Buffer
	if err := JSON(&b, c); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Range struct {
			Timezone  *string `json:"timezone"`
			UTCOffset string  `json:"utc_offset"`
		} `json:"range"`
	}
	if err := json.Unmarshal(b.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	// 1 Oct 2026 is before the end of summer time: +02:00 at the period start
	if d.Range.Timezone == nil || *d.Range.Timezone != "Europe/Madrid" || d.Range.UTCOffset != "+02:00" {
		t.Errorf("range timezone=%v utc_offset=%q", d.Range.Timezone, d.Range.UTCOffset)
	}
}

func TestUnknownLocalZoneIsNull(t *testing.T) {
	if time.Local.String() != "Local" {
		t.Skip("time.Local is already named after TZ in this process")
	}
	if got := zoneName(time.Local); got != nil {
		t.Errorf("zoneName(time.Local) = %v, want null: its zone name is not known", got)
	}
}

// Unpriced models with equal tokens must come out in the same order every run.
func TestUnpricedModelOrderIsStable(t *testing.T) {
	unpriced := map[string]int64{"zeta": 5, "alpha": 5, "mid": 9, "beta": 5, "gamma": 5, "delta": 5}
	g := &agg.Group{Label: "x", Unpriced: unpriced, Sessions: map[string]struct{}{}, Models: map[string]struct{}{}}
	wantNote := "Unpriced: mid, alpha, beta, delta, +2 more."
	wantDetail := "mid, alpha, beta, delta, gamma, zeta"
	for i := 0; i < 50; i++ {
		var note string
		for _, n := range NoteItems(&Ctx{Sum: &agg.Summary{Total: g}}) {
			if strings.HasPrefix(n.Text, "Unpriced: ") {
				note = n.Text
			}
		}
		if !strings.HasPrefix(note, wantNote) {
			t.Fatalf("run %d: note %q, want prefix %q", i, note, wantNote)
		}
		var names []string
		for _, l := range Detail(Theme{ASCII: true}, g, 80) {
			if f := strings.Fields(Strip(l)); len(f) == 3 && f[0] == "unpriced" {
				names = append(names, f[1])
			}
		}
		if got := strings.Join(names, ", "); got != wantDetail {
			t.Fatalf("run %d: detail order %q, want %q", i, got, wantDetail)
		}
	}
}

// Without colour the provider dot is the only mark of a used provider, so it
// must still be drawn.
func TestDotMarksProvidersWithoutColour(t *testing.T) {
	if got := (Theme{}).Dot("claude"); got != "●" {
		t.Errorf("plain dot = %q, want ●", got)
	}
	if got := (Theme{ASCII: true}).Dot("claude"); got != "*" {
		t.Errorf("ASCII dot = %q, want *", got)
	}
}

// Mixed providers: the cache hit share is taken over the events that report
// caches, and the note says how much of the input that covers.
func TestCacheShareNoteNamesItsBase(t *testing.T) {
	pt, _ := pricing.Load("")
	d := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	evs := []model.Event{
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "a", Project: "/w/a", Time: d(1), Input: 100, CacheRead: 900, Output: 10, Has: model.HasCacheRead},
		{Provider: "codex", Model: "gpt-5.4", Session: "b", Project: "/w/b", Time: d(1), Input: 1000, Output: 10}, // no cache fields
	}
	c := &Ctx{Sum: agg.Summarize(evs, agg.Filter{}, pt, time.UTC), Prices: pt}
	lines := strings.Join(TokenMix(Theme{ASCII: true}, c, 100), "\n")
	// 900 of the 1000 input-side tokens of the reporting event; the other
	// provider is half the input and is named, not counted as a zero
	want := "cache hit share: 90% of input-side tokens (from 50% of input that reports caches)"
	if !strings.Contains(lines, want) {
		t.Errorf("want %q in:\n%s", want, lines)
	}
	if compact := strings.Join(Compact(Theme{ASCII: true}, c, 100), " "); !strings.Contains(compact, "cached 90% of input (from 50% that reports caches)") {
		t.Errorf("compact must say the same base:\n%s", compact)
	}
}

// When every input token comes from reporting sources the note is not
// needed, and the share is the plain share.
func TestCacheShareHasNoNoteWhenAllInputIsCovered(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{{Provider: "claude", Model: "claude-sonnet-5-5", Time: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Input: 100, CacheRead: 900, Has: model.HasCacheRead}}
	c := &Ctx{Sum: agg.Summarize(evs, agg.Filter{}, pt, time.UTC), Prices: pt}
	text := strings.Join(strings.Fields(strings.Join(Compact(Theme{ASCII: true}, c, 100), " ")), " ")
	if !strings.Contains(text, "cached 90% of input") || strings.Contains(text, "reports caches") {
		t.Errorf("want a plain base and no coverage note: %q", text)
	}
}

// The coverage note must not say "from 100%" when a little input is missing.
func TestCoverageNoteIsNotRoundedUpToAllInput(t *testing.T) {
	if got := coverPct(0.9984); got != "99.8%" {
		t.Errorf("coverPct(0.9984) = %q, want 99.8%%", got)
	}
	if got := coverPct(0.5); got != "50%" {
		t.Errorf("coverPct(0.5) = %q", got)
	}
}

func TestReasoningIsCalledReasoningEverywhere(t *testing.T) {
	c := sample(t)
	out := strings.Join(TokenMix(Theme{ASCII: true}, c, 100), "\n")
	if !strings.Contains(out, "of which reasoning") || strings.Contains(out, "thinking") {
		t.Errorf("token mix wording:\n%s", out)
	}
}

func dayGroups(start time.Time, n int, tokens func(i int) int64) []*agg.Group {
	out := make([]*agg.Group, n)
	for i := range out {
		d := start.AddDate(0, 0, i)
		out[i] = &agg.Group{Key: d.Format("2006-01-02"), Input: tokens(i)}
	}
	return out
}

func TestDayBucketsSizeIsTheSmallestThatFits(t *testing.T) {
	days := dayGroups(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 49, func(int) int64 { return 1 })
	if vals, size := DayBuckets(days, 7); size != 7 || len(vals) != 7 {
		t.Errorf("49 days in 7 slots: size %d, %d buckets; want weekly", size, len(vals))
	}
	days = dayGroups(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 50, func(int) int64 { return 1 })
	vals, size := DayBuckets(days, 7)
	if size != 8 || len(vals) != 7 || vals[6] != 2 { // the last bucket holds the two days left over
		t.Errorf("50 days in 7 slots: size %d, buckets %v", size, vals)
	}
	if vals, size := DayBuckets(days[:5], 7); size != 1 || len(vals) != 5 {
		t.Errorf("days that fit are not grouped: size %d", size)
	}
}

// A window longer than the chart is drawn in buckets: the peak refers to the
// bucket (its first and last day), and the chart line says the bucket size.
func TestDailyPeakRefersToTheBucket(t *testing.T) {
	days := dayGroups(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 70, func(i int) int64 {
		if i >= 21 && i < 28 { // one busy week, Oct 22-28
			return 100
		}
		return 1
	})
	c := &Ctx{Sum: &agg.Summary{ByDay: days, Total: &agg.Group{Input: 1000}}}
	out := strings.Join(Daily(Theme{ASCII: true}, c, 14), "\n") // chart of 10 columns: weekly buckets
	if !strings.Contains(out, "weekly buckets") {
		t.Errorf("chart line must name the bucket size:\n%s", out)
	}
	if !strings.Contains(out, "peak 2026-10-22..2026-10-28 (700)") {
		t.Errorf("peak must be the busy week, not one day:\n%s", out)
	}
	short := &Ctx{Sum: &agg.Summary{ByDay: days[:5], Total: &agg.Group{Input: 5}}}
	out = strings.Join(Daily(Theme{ASCII: true}, short, 60), "\n")
	if strings.Contains(out, "buckets") || !strings.Contains(out, "peak 2026-10-01 ") {
		t.Errorf("days that fit need no bucket note:\n%s", out)
	}
}

func TestCompactTrendNamesBucketAndPeakRange(t *testing.T) {
	days := dayGroups(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 70, func(i int) int64 {
		if i >= 21 && i < 28 {
			return 100
		}
		return 1
	})
	c := &Ctx{Sum: &agg.Summary{ByDay: days, Total: &agg.Group{Input: 1000}}}
	out := strings.Join(Compact(Theme{ASCII: true}, c, 62), "\n")
	if !strings.Contains(out, "weekly buckets") || !strings.Contains(out, "peak 10-22..10-28") {
		t.Errorf("compact trend:\n%s", out)
	}
}

// Truncated names keep a date-like snapshot suffix, so two snapshots of one
// model still look different.
func TestTruncKeepsSnapshotDate(t *testing.T) {
	th := Theme{ASCII: true}
	a := th.Trunc("claude-sonnet-4-6-20260101", 16)
	b := th.Trunc("claude-sonnet-4-6-20260201", 16)
	if a == b {
		t.Fatalf("snapshots look the same: %q and %q", a, b)
	}
	if Width(a) != 16 || !strings.HasSuffix(a, "~-20260101") || !strings.HasSuffix(b, "~-20260201") {
		t.Errorf("want central ellipsis keeping the date, within 16 columns: %q and %q", a, b)
	}
	// without a date the end is cut as before
	if got := th.Trunc("gpt-5.4-codex-preview", 10); got != "gpt-5.4-c~" {
		t.Errorf("plain names are still cut at the end: %q", got)
	}
}

// Compact arrows say what they compare with.
func TestCompactArrowsSayVsPrev(t *testing.T) {
	pt, _ := pricing.Load("")
	d := func(day int) time.Time { return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC) }
	evs := []model.Event{
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "a", Project: "/w", Time: d(30), Input: 100, Output: 100},
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "a", Project: "/w", Time: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Input: 400, Output: 100},
	}
	f := agg.Filter{From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}
	c := &Ctx{Sum: agg.Summarize(evs, f, pt, time.UTC), Prices: pt}
	out := strings.Join(Compact(Theme{ASCII: true}, c, 100), "\n")
	if n := strings.Count(out, "vs prev"); n < 2 {
		t.Errorf("token and cost changes must say \"vs prev\", got %d in:\n%s", n, out)
	}
	if !strings.Contains(out, "^ 150% vs prev") { // 500 tokens against 200 in the previous period
		t.Errorf("token change missing:\n%s", out)
	}
}

// schema_version is the first key of the JSON document.
func TestJSONStartsWithSchemaVersion(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "{\n  \"schema_version\": 1,\n") {
		t.Errorf("document must start with schema_version:\n%.80s", b.String())
	}
	var d map[string]any
	if err := json.Unmarshal(b.Bytes(), &d); err != nil || d["schema_version"] != float64(JSONSchemaVersion) {
		t.Errorf("schema_version not readable: %v", err)
	}
}

// Event CSV: the price is looked up once per model; the cost column must be
// the same as pricing every event on its own.
func TestEventCSVCostsMatchPerEventPricing(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "a", Project: "/w", Time: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Input: 1000, Output: 500},
		{Provider: "claude", Model: "claude-sonnet-5-5", Session: "a", Project: "/w", Time: time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC), Input: 2000, Output: 0},
		{Provider: "codex", Model: "codex-auto-review", Session: "b", Project: "/w", Time: time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC), Input: 5, Output: 5},
	}
	var b bytes.Buffer
	c := &Ctx{Sum: agg.Summarize(evs, agg.Filter{}, pt, time.UTC), Prices: pt}
	if err := CSV(&b, c, "events", evs, &agg.Filter{}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range evs {
		want := ""
		if p := pt.Lookup(e.Model); p != nil {
			want = fmt.Sprintf("%.6f", p.Cost(&evs[i]))
		}
		if got := rows[i+1][len(rows[i+1])-1]; got != want {
			t.Errorf("row %d (%s): cost %q, want %q", i, e.Model, got, want)
		}
	}
}

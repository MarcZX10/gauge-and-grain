package agg

import (
	"fmt"
	"math"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
)

var utc = time.UTC

func ev(day int, prov, mdl, proj, sess string, in, out int64) model.Event {
	return model.Event{Provider: prov, Model: mdl, Project: proj, Session: sess,
		Time: time.Date(2026, 10, day, 12, 0, 0, 0, utc), Input: in, Output: out}
}

func TestFiltersCombineAndRangeIsHalfOpen(t *testing.T) {
	evs := []model.Event{
		ev(1, "claude", "claude-sonnet-5-5", "/a/web", "s1", 10, 10),
		ev(2, "codex", "gpt-5.4", "/a/api", "s2", 20, 20),
		ev(3, "codex", "gpt-6-sol", "/b/web", "s3", 30, 30),
	}
	pt, _ := pricing.Load("")
	f := Filter{From: time.Date(2026, 10, 2, 0, 0, 0, 0, utc), To: time.Date(2026, 10, 3, 0, 0, 0, 0, utc)}
	if s := Summarize(evs, f, pt, utc); s.Total.Events != 1 || s.Total.Tokens() != 40 {
		t.Errorf("To must be exclusive: %+v", s.Total)
	}
	s := Summarize(evs, Filter{Providers: []string{"codex"}, Projects: []string{"WEB"}}, pt, utc)
	if s.Total.Events != 1 || s.Total.Tokens() != 60 {
		t.Errorf("provider AND project (case-insensitive): %+v", s.Total)
	}
	if Summarize(evs, Filter{Text: "api"}, pt, utc).Total.Events != 1 {
		t.Error("free text must match project")
	}
	if Summarize(evs, Filter{Models: []string{"gpt"}}, pt, utc).Total.Events != 2 {
		t.Error("model substring")
	}
}

func TestUnpricedIsNeverZeroCost(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{
		ev(1, "claude", "claude-sonnet-5-5", "/a", "s1", 1_000_000, 0),
		ev(1, "codex", "codex-auto-review", "/a", "s2", 500, 500),
	}
	s := Summarize(evs, Filter{}, pt, utc)
	if s.Total.Complete() || s.Total.UnpricedTok != 1000 || s.Total.Cost != 2.0 {
		t.Errorf("total: cost=%v unpriced=%d", s.Total.Cost, s.Total.UnpricedTok)
	}
	for _, g := range s.ByProvider {
		if g.Key == "codex" && (g.CostKnown() && g.PricedTok != 0 || g.Complete()) {
			t.Errorf("codex group must be fully unpriced: %+v", g)
		}
	}
}

func TestPreviousPeriodAndChange(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{
		ev(1, "claude", "claude-sonnet-5-5", "/a", "s", 50, 50),   // previous week
		ev(8, "claude", "claude-sonnet-5-5", "/a", "s", 100, 100), // this week
	}
	f := Filter{From: time.Date(2026, 10, 8, 0, 0, 0, 0, utc), To: time.Date(2026, 10, 15, 0, 0, 0, 0, utc)}
	s := Summarize(evs, f, pt, utc)
	if s.Prev == nil || s.Prev.Tokens() != 100 {
		t.Fatalf("prev: %+v", s.Prev)
	}
	if ch, ok := Change(float64(s.Total.Tokens()), float64(s.Prev.Tokens())); !ok || ch != 1.0 {
		t.Errorf("change=%v ok=%v", ch, ok)
	}
	if _, ok := Change(5, 0); ok {
		t.Error("no change figure against an empty previous period")
	}
	if Summarize(evs, Filter{}, pt, utc).Prev != nil {
		t.Error("unbounded range has no previous period")
	}
}

func TestDailyFillsGapsInLocalTime(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{ev(1, "claude", "m", "/a", "s", 1, 1), ev(4, "claude", "m", "/a", "s", 1, 1)}
	s := Summarize(evs, Filter{}, pt, utc)
	if len(s.ByDay) != 4 || s.ByDay[1].Tokens() != 0 {
		t.Errorf("want 4 days with zero-filled gaps, got %d", len(s.ByDay))
	}
	late := model.Event{Provider: "claude", Model: "m", Session: "s", Time: time.Date(2026, 10, 1, 23, 30, 0, 0, utc), Input: 1}
	tokyo := time.FixedZone("JST", 9*3600)
	if d := Summarize([]model.Event{late}, Filter{}, pt, tokyo).ByDay[0].Key; d != "2026-10-02" {
		t.Errorf("day buckets must use the local zone, got %s", d)
	}
}

func TestSessionsDoNotCollideAcrossProviders(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{ev(1, "claude", "m", "/a", "same", 1, 1), ev(1, "codex", "m", "/a", "same", 1, 1)}
	if n := len(Summarize(evs, Filter{}, pt, utc).Total.Sessions); n != 2 {
		t.Errorf("sessions=%d, want 2", n)
	}
}

func TestCoverageReflectsReportedFieldsOnly(t *testing.T) {
	pt, _ := pricing.Load("")
	a := ev(1, "claude", "m", "/a", "s", 1, 1)
	a.Has = model.HasCacheRead
	b := ev(1, "claude", "m", "/a", "s2", 1, 1)
	cov := Summarize([]model.Event{a, b}, Filter{}, pt, utc).Coverage()
	if len(cov) != 1 || cov[0].Events != 2 || cov[0].CacheRead != 1 || cov[0].Reasoning != 0 {
		t.Errorf("%+v", cov)
	}
}

// The index must give exactly what a naive per-event pass gives, for any
// filter and time zone (day granularity).
func TestIndexEqualsNaivePerEventSummary(t *testing.T) {
	pt, _ := pricing.Load("")
	zones := []*time.Location{time.UTC, time.FixedZone("JST", 9*3600), time.FixedZone("PST", -8*3600)}
	models := []string{"claude-sonnet-5-5", "gpt-5.4", "mystery", "claude-opus-5-5"}
	provs := []string{"claude", "codex"}
	projs := []string{"/w/alpha", "/w/beta", "/x/Gamma", ""}
	rnd := uint64(88172645463325252)
	next := func(n int) int {
		rnd ^= rnd << 13
		rnd ^= rnd >> 7
		rnd ^= rnd << 17
		return int(rnd % uint64(n))
	}
	var evs []model.Event
	for i := 0; i < 6000; i++ {
		evs = append(evs, model.Event{
			Provider: provs[next(2)], Model: models[next(4)], Project: projs[next(4)], Session: fmt.Sprint("s", next(40)),
			Time:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(next(60*24*60)) * time.Minute),
			Input: int64(next(1000)), CacheRead: int64(next(1000)), CacheWrite: int64(next(500)), CacheWrite1h: int64(next(100)),
			Output: int64(next(300)), Reasoning: int64(next(50)), Has: uint8(next(8)),
		})
	}
	for _, loc := range zones {
		idx := NewIndex(evs, pt, loc)
		for n := 0; n < 40; n++ {
			from := time.Date(2026, 9, 1+next(40), 0, 0, 0, 0, loc)
			f := Filter{From: from, To: from.AddDate(0, 0, 1+next(20))}
			if next(3) == 0 {
				f = Filter{}
			}
			if next(2) == 0 {
				f.Providers = []string{provs[next(2)]}
			}
			if next(3) == 0 {
				f.Models = []string{[]string{"GPT", "opus"}[next(2)]}
			}
			if next(3) == 0 {
				f.Projects = []string{"alpha"}
			}
			if next(4) == 0 {
				f.Text = "s1"
			}
			got := idx.Summarize(f)
			var want Group
			wantSess := map[string]bool{}
			var wantCost float64
			var wantUnpriced int64
			for i := range evs {
				e := &evs[i]
				if !f.Match(e) {
					continue
				}
				// Filter.Match works on instants; the index on civil days in loc. Equivalent for day-aligned bounds.
				want.Events++
				want.Input += e.Input
				want.Output += e.Output
				want.CacheRead += e.CacheRead
				want.CacheWrite += e.CacheWrite
				wantSess[e.Provider+"/"+e.Session] = true
				if p := pt.Lookup(e.Model); p != nil {
					wantCost += p.Cost(e)
				} else {
					wantUnpriced += e.Total()
				}
			}
			g := got.Total
			if g.Events != want.Events || g.Input != want.Input || g.Output != want.Output || g.CacheRead != want.CacheRead ||
				g.CacheWrite != want.CacheWrite || len(g.Sessions) != len(wantSess) || g.UnpricedTok != wantUnpriced ||
				math.Abs(g.Cost-wantCost) > 1e-6*math.Max(1, wantCost) {
				t.Fatalf("%s filter %+v:\n index %+v\n naive events=%d in=%d out=%d cr=%d cw=%d sess=%d unpriced=%d cost=%v",
					loc, f, *g, want.Events, want.Input, want.Output, want.CacheRead, want.CacheWrite, len(wantSess), wantUnpriced, wantCost)
			}
		}
	}
}

func BenchmarkSummarizeIndexed(b *testing.B) {
	pt, _ := pricing.Load("")
	var evs []model.Event
	for i := 0; i < 300000; i++ {
		evs = append(evs, model.Event{Provider: "codex", Model: "gpt-5.4", Project: "/w/p", Session: fmt.Sprint("s", i%300),
			Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i%40000) * time.Minute), Input: 10, Output: 5})
	}
	idx := NewIndex(evs, pt, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Summarize(Filter{})
	}
}

func TestSameModelNameFromTwoProvidersIsShownAsTwoDistinctRows(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{
		ev(1, "codex", "gpt-5.4", "/a", "s1", 10, 10),
		ev(1, "opencode", "gpt-5.4", "/a", "s2", 20, 20),
		ev(1, "claude", "claude-sonnet-5-5", "/a", "s3", 5, 5),
	}
	s := Summarize(evs, Filter{}, pt, utc)
	labels := map[string]bool{}
	for _, g := range s.ByModel {
		labels[g.Label] = true
	}
	if !labels["gpt-5.4 (codex)"] || !labels["gpt-5.4 (opencode)"] || !labels["claude-sonnet-5-5"] || len(s.ByModel) != 3 {
		t.Errorf("labels: %v", labels)
	}
}

func TestExactNameFiltersDoNotMatchSubstrings(t *testing.T) {
	evs := []model.Event{
		{Provider: "p", Model: "gpt-5", Project: "/a/app", Time: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), Input: 100},
		{Provider: "p", Model: "gpt-5.4", Project: "/a/app2", Time: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), Input: 900},
	}
	pt, _ := pricing.Load("")
	idx := NewIndex(evs, pt, time.UTC)
	f := Filter{ModelsExact: []string{"GPT-5"}}
	if got := idx.Summarize(f).Total.Tokens(); got != 100 {
		t.Errorf("exact model: %d, want 100", got)
	}
	f = Filter{ProjectsExact: []string{"/a/app"}}
	if got := idx.Summarize(f).Total.Tokens(); got != 100 {
		t.Errorf("exact project: %d, want 100", got)
	}
	// the substring filter is still there for --model / --project
	if got := idx.Summarize(Filter{Models: []string{"gpt-5"}}).Total.Tokens(); got != 1000 {
		t.Errorf("substring model: %d", got)
	}
	ef := Filter{ModelsExact: []string{"gpt-5"}}
	if ef.Match(&evs[1]) || !ef.Match(&evs[0]) {
		t.Error("Match must apply the exact filter too")
	}
}

// Across a daylight-saving change the comparison window is still whole days:
// 20 Oct–2 Nov 2026 in Madrid is 14 days, and the window before it is 14 days.
func TestPreviousWindowIsWholeDaysAcrossDST(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, madrid)
	to := time.Date(2026, 11, 3, 0, 0, 0, 0, madrid)
	pf, pt := PrevWindow(from, to, madrid)
	if DaysBetween(pf, pt, madrid) != 14 || !pt.Equal(from) {
		t.Errorf("previous window %v..%v", pf, pt)
	}
	if pf.Format("2006-01-02") != "2026-10-06" {
		t.Errorf("start %v", pf)
	}
}

// Tool uses are counted per view and ordered by use; sources without tool
// calls contribute nothing.
func TestToolUsesAreCountedPerViewAndOrdered(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	evs := []model.Event{
		{Provider: "claude", Model: "m", Project: "/a", Session: "s1", Time: at, Input: 1, Tools: "Bash,Read,Bash"},
		{Provider: "claude", Model: "m", Project: "/b", Session: "s2", Time: at, Input: 1, Tools: "Read"},
		{Provider: "codex", Model: "m", Project: "/a", Session: "s3", Time: at, Input: 1},
	}
	pt, _ := pricing.Load("")
	idx := NewIndex(evs, pt, time.UTC)
	s := idx.Summarize(Filter{})
	want := []ToolStat{{"Bash", 2}, {"Read", 2}}
	if len(s.Tools) != 2 || s.Tools[0] != want[0] || s.Tools[1] != want[1] {
		t.Errorf("tools: %+v", s.Tools)
	}
	s = idx.Summarize(Filter{Projects: []string{"/b"}})
	if len(s.Tools) != 1 || s.Tools[0].Name != "Read" || s.Tools[0].Uses != 1 {
		t.Errorf("the project filter must apply to tools too: %+v", s.Tools)
	}
}

func TestDailyFillsTheWholeWindow(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{ev(2, "claude", "m", "/a", "s", 1, 1), ev(4, "claude", "m", "/a", "s", 1, 1), ev(6, "claude", "m", "/a", "s", 1, 1)}
	f := Filter{From: time.Date(2026, 10, 1, 0, 0, 0, 0, utc), To: time.Date(2026, 10, 8, 0, 0, 0, 0, utc)}
	s := Summarize(evs, f, pt, utc)
	active := 0
	for _, d := range s.ByDay {
		if d.Tokens() > 0 {
			active++
		}
	}
	if len(s.ByDay) != 7 || active != 3 {
		t.Errorf("7-day window with usage on 3 days: %d rows, %d active", len(s.ByDay), active)
	}
	if s.ByDay[0].Key != "2026-10-01" || s.ByDay[6].Key != "2026-10-07" {
		t.Errorf("window edges: %s .. %s", s.ByDay[0].Key, s.ByDay[6].Key)
	}
	// All time: from the first to the last event day.
	if all := Summarize(evs, Filter{}, pt, utc); len(all.ByDay) != 5 {
		t.Errorf("unbounded: %d rows, want 5 (2..6 Oct)", len(all.ByDay))
	}
}

func TestDailyWindowIsClampedToToday(t *testing.T) {
	m := map[string]*Group{"2026-10-02": newGroup("2026-10-02", "x")}
	f := Filter{From: time.Date(2026, 10, 1, 0, 0, 0, 0, utc), To: time.Date(2026, 11, 1, 0, 0, 0, 0, utc)}
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, utc)
	days := fillDays(m, f, utc, now)
	if len(days) != 3 || days[2].Key != "2026-10-03" {
		t.Errorf("future days must not be filled: got %d rows, last %s", len(days), days[len(days)-1].Key)
	}
}

func TestMidnightInDSTGapDoesNotIncludeExcludedDay(t *testing.T) {
	// Asia/Beirut skips 00:00 to 01:00 on 2025-03-30, so To = 00:00 normalises to 01:00.
	loc, err := time.LoadLocation("Asia/Beirut")
	if err != nil {
		t.Skip("tz database lacks Asia/Beirut")
	}
	pt, _ := pricing.Load("")
	var evs []model.Event
	for _, d := range []int{29, 30, 31} {
		evs = append(evs, model.Event{Provider: "claude", Model: "m", Session: "s", Input: 1,
			Time: time.Date(2025, 3, d, 12, 0, 0, 0, loc)})
	}
	to := time.Date(2025, 3, 30, 0, 0, 0, 0, loc)
	if s := Summarize(evs, Filter{To: to}, pt, loc); s.Total.Events != 1 {
		t.Errorf("To=2025-03-30 00:00 (gap) must exclude 30 March: %d events", s.Total.Events)
	}
	from := time.Date(2025, 3, 30, 0, 0, 0, 0, loc)
	if s := Summarize(evs, Filter{From: from}, pt, loc); s.Total.Events != 2 {
		t.Errorf("From=2025-03-30 00:00 (gap) must include 30 March: %d events", s.Total.Events)
	}
}

func TestShortIDIsRuneSafe(t *testing.T) {
	got := ShortID("ñáéíóúüçñ-extra")
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 8 {
		t.Errorf("ShortID cut inside a rune: %q", got)
	}
}

func TestSessionLabelsStayUniqueWithinASummary(t *testing.T) {
	// UUIDv7 starts with a timestamp: sessions started close together share a prefix.
	pt, _ := pricing.Load("")
	evs := []model.Event{
		ev(1, "claude", "m", "/a", "0190abcd-1111-7000-8000-000000000001", 1, 1),
		ev(1, "claude", "m", "/a", "0190abcd-2222-7000-8000-000000000002", 1, 1),
	}
	s := Summarize(evs, Filter{}, pt, utc)
	seen := map[string]bool{}
	for _, g := range s.BySession {
		if seen[g.Label] {
			t.Errorf("duplicate session label %q", g.Label)
		}
		seen[g.Label] = true
	}
	if len(seen) != 2 {
		t.Errorf("want 2 distinct session labels, got %v", seen)
	}
}

// A provider that does not report cache reads must not lower the cache hit
// share: the share is taken over the events that report them, and the
// coverage says how much of the input that is.
func TestCacheShareUsesOnlyEventsThatReportCaches(t *testing.T) {
	reports := ev(1, "claude", "claude-sonnet-5-5", "/a", "s1", 100, 10)
	reports.CacheRead, reports.CacheWrite = 900, 0
	reports.Has = model.HasCacheRead
	silent := ev(1, "codex", "gpt-5.4", "/a", "s2", 1000, 10) // reports no caches
	pt, _ := pricing.Load("")
	s := Summarize([]model.Event{reports, silent}, Filter{}, pt, utc)

	share, ok := s.Total.CacheShare()
	if !ok || share != 0.9 {
		t.Errorf("share over reporting events = %v (ok %v), want 0.9", share, ok)
	}
	cov, ok := s.Total.CacheCoverage()
	if !ok || cov != 0.5 {
		t.Errorf("coverage = %v (ok %v), want 0.5 (1000 of 2000 input tokens)", cov, ok)
	}
	for _, g := range s.ByProvider {
		if g.Key == "codex" {
			if _, ok := g.CacheShare(); ok {
				t.Error("a provider that reports no caches has no cache share")
			}
		}
	}
}

// Two projects that end in the same two elements get longer labels until
// they differ, and projects that already differ keep the short label.
func TestProjectLabelsStayUniqueWithinASummary(t *testing.T) {
	pt, _ := pricing.Load("")
	evs := []model.Event{
		ev(1, "claude", "claude-sonnet-5-5", "/home/a/x/app", "s1", 10, 0),
		ev(1, "claude", "claude-sonnet-5-5", "/home/b/x/app", "s2", 10, 0),
		ev(1, "claude", "claude-sonnet-5-5", "/work/api", "s3", 10, 0),
	}
	s := Summarize(evs, Filter{}, pt, utc)
	labels := map[string]string{}
	for _, g := range s.ByProject {
		labels[g.Key] = g.Label
	}
	if labels["/home/a/x/app"] != "a/x/app" || labels["/home/b/x/app"] != "b/x/app" {
		t.Errorf("labels must be unique: %v", labels)
	}
	if labels["/work/api"] != "work/api" {
		t.Errorf("unambiguous project keeps the short label: %v", labels)
	}
}

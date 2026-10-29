package agg

import (
	"fmt"
	"math"
	"testing"
	"time"

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

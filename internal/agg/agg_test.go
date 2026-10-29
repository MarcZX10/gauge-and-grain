package agg

import (
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

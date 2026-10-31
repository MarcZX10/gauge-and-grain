package main

import (
	"strings"
	"testing"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/render"
)

// The change arithmetic: a cost change is stated only when both sides are
// fully priced, and a zero base gives "new" rather than a percentage.
func TestCompareChangeArithmetic(t *testing.T) {
	th := render.Theme{}
	priced := func(cost float64, tok int64) *agg.Group {
		return &agg.Group{Cost: cost, PricedTok: tok, Input: tok}
	}
	unpriced := &agg.Group{Input: 100, UnpricedTok: 100}
	cases := []struct {
		name string
		a, b *agg.Group
		want string
	}{
		{"up 50%", priced(2, 100), priced(3, 150), "▲ 50%"},
		{"down 25%", priced(4, 100), priced(3, 100), "▼ 25%"},
		{"flat", priced(2, 100), priced(2, 100), "■ flat"},
		{"zero base is new", &agg.Group{}, priced(1, 100), "new"},
		{"both zero", &agg.Group{}, &agg.Group{}, "—"},
		{"A has unpriced usage", unpriced, priced(3, 150), "n/a"},
		{"B has unpriced usage", priced(2, 100), unpriced, "n/a"},
	}
	for _, c := range cases {
		if got := changeText(th, c.a.Cost, c.b.Cost, costKnown(c.a, c.b)); got != c.want {
			t.Errorf("%s: cost change = %q, want %q", c.name, got, c.want)
		}
	}
	if got := changeText(th, 100, 150, true); got != "▲ 50%" {
		t.Errorf("token change = %q, want ▲ 50%%", got)
	}
	if got := changeText(th, 100, 150, false); got != "n/a" {
		t.Errorf("unknown token change = %q, want n/a", got)
	}
	a := &agg.Group{Input: 100, CacheRead: 100} // 50% cache share
	b := &agg.Group{Input: 100, CacheRead: 300} // 75% cache share
	if got := shareChange(a, b); got != "+25.0 pts" {
		t.Errorf("cache share change = %q, want +25.0 pts", got)
	}
	if got := shareChange(a, &agg.Group{}); got != "n/a" {
		t.Errorf("cache share with no data = %q, want n/a", got)
	}
}

// The output names both periods with their dates, and the cost change is n/a
// because B has unpriced usage.
func TestCompareOutputNamesBothPeriods(t *testing.T) {
	at := func(day int, name string, in, out int64) model.Event {
		return model.Event{Provider: "claude", Model: name, Session: "s1", Project: "/p/app",
			Time: time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC), Input: in, Output: out}
	}
	events := []model.Event{
		{Provider: "claude", Model: "claude-sonnet-4-5", Session: "s0", Project: "/p/app",
			Time: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), Key: "sep", Input: 1000000, Output: 100000},
		at(3, "claude-sonnet-4-5", 2000000, 200000),
		at(4, "mystery-model-9", 500000, 0),
	}
	pt, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	args := []string{"2026-09", "2026-10"}
	filters, err := compareFilters(opts{}, args, now)
	if err != nil {
		t.Fatal(err)
	}
	out := compareReport(render.Theme{}, summarizeSides(events, args, filters, pt, now.Location()), "")
	for _, want := range []string{"A  2026-09", "B  2026-10", "1 Sep – 30 Sep 2026", "1 Oct – 31 Oct 2026",
		"TOTALS", "PROVIDERS", "MODELS", "claude-sonnet-4-5", "mystery-model-9", "Est. cost", "n/a"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

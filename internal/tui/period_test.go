package tui

import (
	"testing"
	"time"
)

var jst = time.FixedZone("JST", 9*3600)

func TestPresetWindows(t *testing.T) {
	now := time.Date(2026, 10, 29, 15, 30, 0, 0, jst)
	for _, c := range []struct {
		id       string
		shift    int
		from, to string
	}{
		{"today", 0, "2026-10-29", "2026-10-30"},
		{"today", 1, "2026-10-28", "2026-10-29"},
		{"7d", 0, "2026-10-23", "2026-10-30"},
		{"7d", 1, "2026-10-16", "2026-10-23"},
		{"30d", 0, "2026-09-30", "2026-10-30"},
		{"month", 0, "2026-10-01", "2026-11-01"},
		{"month", 1, "2026-09-01", "2026-10-01"},
		{"month", 10, "2025-12-01", "2026-01-01"},
		{"year", 1, "2025-01-01", "2026-01-01"},
	} {
		f, to := presets[presetIndex(c.id)].span(now, jst, c.shift)
		if f.Format("2006-01-02") != c.from || to.Format("2006-01-02") != c.to || f.Location() != jst {
			t.Errorf("%s shift %d: got %s → %s, want %s → %s", c.id, c.shift, f.Format("2006-01-02"), to.Format("2006-01-02"), c.from, c.to)
		}
	}
	if f, to := presets[presetIndex("all")].span(now, jst, 0); !f.IsZero() || !to.IsZero() {
		t.Error("all time is open-ended")
	}
}

func TestParsePeriod(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	ok := map[string][2]string{
		"14d":                      {"2026-10-16", "2026-10-30"},
		"14 days":                  {"2026-10-16", "2026-10-30"},
		"2026-10-05":               {"2026-10-05", "2026-10-06"},
		"2026-10":                  {"2026-10-01", "2026-11-01"},
		"2026":                     {"2026-01-01", "2027-01-01"},
		"2026-10-01..2026-10-15":   {"2026-10-01", "2026-10-16"},
		"2026-10-01 to 2026-10-15": {"2026-10-01", "2026-10-16"},
		"2026-09 2026-10":          {"2026-09-01", "2026-11-01"},
	}
	for in, want := range ok {
		p, err := parsePeriod(in, now, time.UTC)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if p.from.Format("2006-01-02") != want[0] || p.to.Format("2006-01-02") != want[1] {
			t.Errorf("%q: %s → %s, want %v", in, p.from.Format("2006-01-02"), p.to.Format("2006-01-02"), want)
		}
	}
	if p, err := parsePeriod("ALL", now, time.UTC); err != nil || presets[p.preset].id != "all" {
		t.Error("all")
	}
	for _, bad := range []string{"", "nope", "2026-13", "2026-10-40", "0d", "2026-10-15..2026-10-01", "1 2 3", "99999d"} {
		if _, err := parsePeriod(bad, now, time.UTC); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestShiftCustomAndMonthRanges(t *testing.T) {
	loc := time.UTC
	p, _ := parsePeriod("2026-10", time.Now(), loc)
	p = p.shifted(-1)
	if p.from.Format("2006-01-02") != "2026-09-01" || p.to.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("a whole month moves by whole months: %v → %v", p.from, p.to)
	}
	q, _ := parsePeriod("2026-10-01..2026-10-10", time.Now(), loc)
	q = q.shifted(-1)
	if q.from.Format("2006-01-02") != "2026-09-21" || q.to.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("a 10-day range moves by 10 days: %v → %v", q.from, q.to)
	}
	a := periodState{preset: presetIndex("all")}
	if a.canShift() || a.shifted(-1).shift != 0 {
		t.Error("all time cannot be shifted")
	}
}

func TestSpanText(t *testing.T) {
	d := func(s string) time.Time { x, _ := time.ParseInLocation("2006-01-02", s, time.UTC); return x }
	for _, c := range []struct{ f, to, want string }{
		{"2026-10-29", "2026-10-30", "29 Oct 2026"},
		{"2026-10-01", "2026-10-16", "1 – 15 Oct 2026"},
		{"2026-09-30", "2026-10-30", "30 Sep – 29 Oct 2026"},
		{"2025-12-15", "2026-01-15", "15 Dec 2025 – 14 Jan 2026"},
	} {
		if got := spanText(d(c.f), d(c.to), time.UTC); got != c.want {
			t.Errorf("%s..%s: %q want %q", c.f, c.to, got, c.want)
		}
	}
}

func TestCalendarPeriodsCompareWithTheLastWholePeriod(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	p := periodState{preset: presetIndex("month")}
	f, to := p.previous(now, time.UTC)
	if f.Format("2006-01-02") != "2026-09-01" || to.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("this month should compare with September, got %v → %v", f, to)
	}
	p.shift = 1
	if f, _ := p.previous(now, time.UTC); f.Format("2006-01-02") != "2026-08-01" {
		t.Errorf("shifted: %v", f)
	}
	if f, _ := (periodState{preset: customPreset}).previous(now, time.UTC); !f.IsZero() {
		t.Error("custom ranges use the equal-length window")
	}
	if got := spanShort(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), time.UTC); got != "31 Aug–29 Sep" {
		t.Errorf("spanShort %q", got)
	}
}

package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
)

// A period is what the user means by "the time I am looking at". It is
// either a preset (a rolling or calendar window, optionally shifted back in
// time) or a custom date range.

type preset struct {
	id    string
	chip  string // short label in the header ("" = not shown as a chip)
	label string // name in lists and the status line
	span  func(now time.Time, loc *time.Location, shift int) (from, to time.Time)
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func rolling(n int) func(time.Time, *time.Location, int) (time.Time, time.Time) {
	return func(now time.Time, loc *time.Location, shift int) (time.Time, time.Time) {
		to := dayStart(now, loc).AddDate(0, 0, 1-shift*n)
		return to.AddDate(0, 0, -n), to
	}
}

var presets = []preset{
	{"today", "Today", "Today", rolling(1)},
	{"7d", "7d", "Last 7 days", rolling(7)},
	{"30d", "30d", "Last 30 days", rolling(30)},
	{"90d", "90d", "Last 90 days", rolling(90)},
	{"month", "", "This month", func(now time.Time, loc *time.Location, shift int) (time.Time, time.Time) {
		n := now.In(loc)
		from := time.Date(n.Year(), n.Month()-time.Month(shift), 1, 0, 0, 0, 0, loc)
		return from, from.AddDate(0, 1, 0)
	}},
	{"year", "", "This year", func(now time.Time, loc *time.Location, shift int) (time.Time, time.Time) {
		from := time.Date(now.In(loc).Year()-shift, 1, 1, 0, 0, 0, 0, loc)
		return from, from.AddDate(1, 0, 0)
	}},
	{"all", "All", "All time", func(time.Time, *time.Location, int) (time.Time, time.Time) { return time.Time{}, time.Time{} }},
}

const customPreset = -1

func presetIndex(id string) int {
	for i, p := range presets {
		if p.id == id {
			return i
		}
	}
	return 0
}

// periodState is the selected period.
type periodState struct {
	preset int // index into presets, or customPreset
	shift  int // how many windows back (presets only)
	from   time.Time
	to     time.Time // exclusive
	label  string    // custom ranges only
}

// window returns [from, to); both zero means "all time". A custom range is
// its typed dates moved back by shift windows of its own length.
func (p periodState) window(now time.Time, loc *time.Location) (time.Time, time.Time) {
	if p.preset == customPreset {
		return moveWindow(p.from, p.to, loc, -p.shift)
	}
	return presets[p.preset].span(now, loc, p.shift)
}

// moveWindow shifts [from, to) by k times its own length, in whole local
// calendar days (or whole months for a month range). Durations are not added:
// across a daylight-saving change a 14-day range is 14 days and an hour long.
func moveWindow(from, to time.Time, loc *time.Location, k int) (time.Time, time.Time) {
	if to.Sub(from) >= 28*24*time.Hour && isWholeMonths(from.In(loc), to.In(loc)) {
		months := monthsBetween(from.In(loc), to.In(loc))
		return from.In(loc).AddDate(0, k*months, 0), to.In(loc).AddDate(0, k*months, 0)
	}
	n := agg.DaysBetween(from, to, loc)
	return addDaysIn(from, loc, k*n), addDaysIn(to, loc, k*n)
}

// addDaysIn moves t by whole calendar days in loc, keeping its wall-clock time.
func addDaysIn(t time.Time, loc *time.Location, n int) time.Time {
	y, m, d := t.In(loc).Date()
	h, mi, s := t.In(loc).Clock()
	return time.Date(y, m, d+n, h, mi, s, t.Nanosecond(), loc)
}

func (p periodState) name() string {
	n := p.label
	if p.preset != customPreset {
		n = presets[p.preset].label
	}
	if p.shift > 0 {
		n += fmt.Sprintf(" ◂%d", p.shift)
	}
	return n
}

// canShift reports whether moving through time makes sense.
func (p periodState) canShift() bool {
	return p.preset == customPreset || presets[p.preset].id != "all"
}

// shifted moves the window by one period length: delta -1 is earlier, +1
// later. It never goes past the period as typed or chosen, so the window
// cannot slide into the future.
func (p periodState) shifted(delta int) periodState {
	if !p.canShift() {
		return p
	}
	p.shift -= delta // earlier (-1) means one more window back
	if p.shift < 0 {
		p.shift = 0
	}
	return p
}

func isWholeMonths(from, to time.Time) bool {
	return from.Day() == 1 && to.Day() == 1 && from.Hour()+to.Hour() == 0
}

func monthsBetween(from, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()-from.Month())
}

// spanText renders a window as dates: "30 Sep – 29 Oct 2026", "29 Oct 2026".
func spanText(from, to time.Time, loc *time.Location) string {
	if from.IsZero() && to.IsZero() {
		return ""
	}
	last := to.AddDate(0, 0, -1).In(loc)
	f := from.In(loc)
	switch {
	case dayStart(f, loc).Equal(dayStart(last, loc)):
		return f.Format("2 Jan 2006")
	case f.Year() == last.Year() && f.Month() == last.Month():
		return f.Format("2") + " – " + last.Format("2 Jan 2006")
	case f.Year() == last.Year():
		return f.Format("2 Jan") + " – " + last.Format("2 Jan 2006")
	}
	return f.Format("2 Jan 2006") + " – " + last.Format("2 Jan 2006")
}

// pickerEntries are the choices in the period picker.
type pickEntry struct {
	label string
	p     periodState
}

func pickerEntries() []pickEntry {
	at := func(id string, shift int) periodState { return periodState{preset: presetIndex(id), shift: shift} }
	return []pickEntry{
		{"Today", at("today", 0)},
		{"Yesterday", at("today", 1)},
		{"Last 7 days", at("7d", 0)},
		{"Last 30 days", at("30d", 0)},
		{"Last 90 days", at("90d", 0)},
		{"This month", at("month", 0)},
		{"Last month", at("month", 1)},
		{"This year", at("year", 0)},
		{"All time", at("all", 0)},
	}
}

// parsePeriod understands what people type:
//
//	14d  |  14 days        the last 14 days
//	2026-10-29             one day
//	2026-10                a month
//	2026                   a year
//	2026-10-01..2026-10-15 (or "A B", "A to B")   inclusive range
//	all
func parsePeriod(s string, now time.Time, loc *time.Location) (periodState, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return periodState{}, fmt.Errorf("type a period")
	}
	if s == "all" {
		return periodState{preset: presetIndex("all")}, nil
	}
	norm := strings.NewReplacer("..", " ", " to ", " ", "→", " ", "–", " ", "—", " ").Replace(s)
	f := strings.Fields(norm)
	if len(f) == 2 && (f[1] == "d" || f[1] == "day" || f[1] == "days") {
		f = []string{f[0] + "d"}
	}
	if len(f) == 1 && strings.HasSuffix(f[0], "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(f[0], "d"))
		if err != nil || n < 1 || n > 36500 {
			return periodState{}, fmt.Errorf("%q: use 14d for the last 14 days", s)
		}
		to := dayStart(now, loc).AddDate(0, 0, 1)
		return periodState{preset: customPreset, from: to.AddDate(0, 0, -n), to: to, label: fmt.Sprintf("Last %d days", n)}, nil
	}
	switch len(f) {
	case 1:
		from, to, err := parseUnit(f[0], loc)
		if err != nil {
			return periodState{}, err
		}
		return periodState{preset: customPreset, from: from, to: to, label: unitLabel(f[0])}, nil
	case 2:
		a, _, err := parseUnit(f[0], loc)
		if err != nil {
			return periodState{}, err
		}
		_, b, err := parseUnit(f[1], loc)
		if err != nil {
			return periodState{}, err
		}
		if !b.After(a) {
			return periodState{}, fmt.Errorf("the end must not be before the start")
		}
		return periodState{preset: customPreset, from: a, to: b, label: "Custom range"}, nil
	}
	return periodState{}, fmt.Errorf("%q: try 14d, 2026-10, 2026-10-01..2026-10-15 or all", s)
}

func unitLabel(s string) string {
	switch len(s) {
	case 4:
		return s
	case 7:
		t, _ := time.Parse("2006-01", s)
		return t.Format("January 2006")
	}
	return s
}

// parseUnit parses a year, month or day into its [from, to) window.
func parseUnit(s string, loc *time.Location) (time.Time, time.Time, error) {
	bad := fmt.Errorf("%q is not a date: use YYYY, YYYY-MM or YYYY-MM-DD", s)
	switch len(s) {
	case 4:
		y, err := strconv.Atoi(s)
		if err != nil || y < 1970 || y > 2200 {
			return time.Time{}, time.Time{}, bad
		}
		from := time.Date(y, 1, 1, 0, 0, 0, 0, loc)
		return from, from.AddDate(1, 0, 0), nil
	case 7:
		t, err := time.ParseInLocation("2006-01", s, loc)
		if err != nil {
			return time.Time{}, time.Time{}, bad
		}
		return t, t.AddDate(0, 1, 0), nil
	case 10:
		t, err := time.ParseInLocation("2006-01-02", s, loc)
		if err != nil {
			return time.Time{}, time.Time{}, bad
		}
		return t, t.AddDate(0, 0, 1), nil
	}
	return time.Time{}, time.Time{}, bad
}

// previous returns the natural comparison window for a period: the same
// preset one step further back (so "this month" compares with last month,
// not with "the 31 days before"). Custom ranges return zero values and use
// the equal-length window just before them.
func (p periodState) previous(now time.Time, loc *time.Location) (time.Time, time.Time) {
	if p.preset == customPreset || presets[p.preset].id == "all" {
		return time.Time{}, time.Time{}
	}
	return presets[p.preset].span(now, loc, p.shift+1)
}

// spanShort is spanText for narrow places: "31 Aug–29 Sep".
func spanShort(from, to time.Time, loc *time.Location) string {
	last := to.AddDate(0, 0, -1).In(loc)
	f := from.In(loc)
	switch {
	case dayStart(f, loc).Equal(dayStart(last, loc)):
		return f.Format("2 Jan")
	case f.Month() == last.Month() && f.Year() == last.Year():
		return f.Format("2") + "–" + last.Format("2 Jan")
	}
	return f.Format("2 Jan") + "–" + last.Format("2 Jan")
}

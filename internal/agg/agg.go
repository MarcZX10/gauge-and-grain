// Package agg turns events into the groupings, comparisons and coverage
// statements shown by the report, the TUI and the exports.
package agg

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
)

// Filter selects events. Empty fields match everything. To is exclusive.
type Filter struct {
	From, To  time.Time
	Providers []string // exact provider ids
	Models    []string // case-insensitive substrings, any may match
	Projects  []string // case-insensitive substrings of the project path
	// ModelsExact and ProjectsExact match whole names (case-insensitive). A
	// drill-down uses them: the row "gpt-5" must not also pull in "gpt-5.4".
	ModelsExact, ProjectsExact []string
	Text                       string // free text: matches model, project or session (TUI search)
	// PrevFrom/PrevTo override the comparison window (exclusive end). Left
	// zero, the comparison is the window of equal length just before From.
	// Calendar periods use it so "this month" compares with the last whole month.
	PrevFrom, PrevTo time.Time
}

// PrevWindow is the window of equal length just before [from, to), counted in
// whole local days. Subtracting durations is wrong across a daylight-saving
// change: 14 days in October can be 14 days and an hour long.
func PrevWindow(from, to time.Time, loc *time.Location) (time.Time, time.Time) {
	n := DaysBetween(from, to, loc)
	return addDays(from, loc, -n), from
}

// DaysBetween counts the local calendar days from the day of from up to the
// day that contains to (a bound inside a day includes that day, as in Filter).
func DaysBetween(from, to time.Time, loc *time.Location) int {
	a, b := dayNum(from, loc), dayNum(to, loc)
	if !atDayStart(to, loc) {
		b++
	}
	return int(b - a)
}

// atDayStart reports whether t is the first instant of its local day. A
// bound given as midnight in a zone with a DST gap there normalises to 01:00,
// so comparing clock readings would wrongly treat it as inside the day.
func atDayStart(t time.Time, loc *time.Location) bool {
	y, m, d := t.In(loc).Date()
	return t.Equal(time.Date(y, m, d, 0, 0, 0, 0, loc))
}

// addDays moves t by whole calendar days in loc, keeping its wall-clock time.
func addDays(t time.Time, loc *time.Location, n int) time.Time {
	y, m, d := t.In(loc).Date()
	h, mi, s := t.In(loc).Clock()
	return time.Date(y, m, d+n, h, mi, s, t.Nanosecond(), loc)
}

// Match reports whether e passes the filter.
func (f *Filter) Match(e *model.Event) bool {
	if !f.From.IsZero() && e.Time.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !e.Time.Before(f.To) {
		return false
	}
	if len(f.Providers) > 0 && !contains(f.Providers, e.Provider) {
		return false
	}
	if len(f.Models) > 0 && !anySub(f.Models, e.Model) {
		return false
	}
	if len(f.Projects) > 0 && !anySub(f.Projects, e.Project) {
		return false
	}
	if len(f.ModelsExact) > 0 && !anyEqual(f.ModelsExact, e.Model) {
		return false
	}
	if len(f.ProjectsExact) > 0 && !anyEqual(f.ProjectsExact, e.Project) {
		return false
	}
	if f.Text != "" && !anySub([]string{f.Text}, e.Model+"\x00"+e.Project+"\x00"+e.Session) {
		return false
	}
	return true
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func anyEqual(names []string, s string) bool {
	for _, v := range names {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func anySub(subs []string, s string) bool {
	s = strings.ToLower(s)
	for _, v := range subs {
		if strings.Contains(s, strings.ToLower(v)) {
			return true
		}
	}
	return false
}

// Group is a set of events with summed counters.
type Group struct {
	Key, Label  string
	Events      int
	Input       int64
	CacheRead   int64
	CacheWrite  int64
	Output      int64
	Reasoning   int64
	Cost        float64 // priced events only
	PricedTok   int64
	UnpricedTok int64
	Has         uint8  // OR of per-event field flags
	HasCount    [3]int // events that reported cache read / cache write / reasoning
	// ShareRead and ShareIn are the cache reads and the input-side tokens of
	// the events that report cache reads: the basis of the cache hit share.
	ShareRead, ShareIn int64
	First, Last        time.Time
	Sessions           map[string]struct{}
	Models             map[string]struct{}
	Providers          map[string]struct{}
	Unpriced           map[string]int64 // unpriced model -> tokens
}

// Tokens is the provider-neutral total.
func (g *Group) Tokens() int64 { return g.Input + g.CacheRead + g.CacheWrite + g.Output }

// CacheShare is cache-read tokens as a share of the input-side tokens of the
// events that report cache reads. Providers that do not report caches are
// left out of both sides, so they cannot drag the share down; CacheCoverage
// says how much of the input that is.
func (g *Group) CacheShare() (float64, bool) {
	if g.ShareIn == 0 {
		return 0, false
	}
	return float64(g.ShareRead) / float64(g.ShareIn), true
}

// CacheCoverage is the fraction of all input-side tokens that comes from
// events reporting cache reads (the part CacheShare is computed over).
func (g *Group) CacheCoverage() (float64, bool) {
	in := g.Input + g.CacheRead + g.CacheWrite
	if in == 0 {
		return 0, false
	}
	return float64(g.ShareIn) / float64(in), true
}

// CostKnown is false when nothing in the group could be priced.
func (g *Group) CostKnown() bool { return g.PricedTok > 0 || g.Tokens() == 0 }

// Complete is false when some tokens could not be priced.
func (g *Group) Complete() bool { return g.UnpricedTok == 0 }

func newGroup(k, l string) *Group {
	return &Group{Key: k, Label: l, Sessions: map[string]struct{}{}, Models: map[string]struct{}{},
		Providers: map[string]struct{}{}, Unpriced: map[string]int64{}}
}

func (g *Group) addCell(c *cell) {
	g.Events += c.events
	g.Input += c.input
	g.CacheRead += c.cacheRead
	g.CacheWrite += c.cacheWrite
	g.Output += c.output
	g.Reasoning += c.reasoning
	g.Has |= c.has
	for i := range g.HasCount {
		g.HasCount[i] += c.hasCount[i]
	}
	g.ShareRead += c.shareRead
	g.ShareIn += c.shareIn
	if g.First.IsZero() || c.first.Before(g.First) {
		g.First = c.first
	}
	if c.last.After(g.Last) {
		g.Last = c.last
	}
	g.Sessions[c.sessKey] = struct{}{}
	g.Models[c.model] = struct{}{}
	g.Providers[c.provider] = struct{}{}
	if c.priced {
		g.Cost += c.cost
		g.PricedTok += c.tokens()
	} else {
		g.UnpricedTok += c.tokens()
		g.Unpriced[c.model] += c.tokens()
	}
}

// Summary is everything a view needs.
type Summary struct {
	Filter Filter
	Total  *Group
	Prev   *Group // same-length period immediately before, if the range is bounded
	// Tools are the tool and skill uses in the view's period, most used first.
	// Only sources that record tool calls (Claude Code) contribute.
	Tools      []ToolStat
	ByDay      []*Group
	ByProvider []*Group
	ByModel    []*Group
	ByProject  []*Group
	BySession  []*Group
	Loc        *time.Location
}

// cell is every event of one (day, provider, model, project, session)
// folded together. Cost is computed once per cell, not once per event,
// because all events in a cell share one model and therefore one price.
type cell struct {
	day                               int32 // civil day number in the index's zone
	provider, model, project, sessKey string
	lowModel, lowProject, lowHay      string
	input, cacheRead, cacheWrite      int64
	output, reasoning                 int64
	cost                              float64
	priced                            bool
	has                               uint8
	hasCount                          [3]int
	shareRead, shareIn                int64 // see Group.ShareRead
	events                            int
	first, last                       time.Time
	tools                             map[string]int64 // tool name -> uses, nil when none
}

// ToolStat is how often one tool (or skill) was used.
type ToolStat struct {
	Name string
	Uses int64
}

func (c *cell) tokens() int64 { return c.input + c.cacheRead + c.cacheWrite + c.output }

// Index pre-aggregates events once so that every filter change, range
// change or drill-down is a pass over a few thousand cells instead of a
// pass (with price lookups) over every event.
//
// Date filters therefore have day granularity in the index's time zone.
type Index struct {
	cells []cell
	loc   *time.Location
	days  map[int32]string // day number -> "2006-01-02 Mon"
}

type cellKey struct {
	day                        int32
	provider, model, proj, ses string
}

// dayNum is the civil day of t in loc, as days since 1970-01-01.
func dayNum(t time.Time, loc *time.Location) int32 {
	y, m, d := t.In(loc).Date()
	return int32(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// NewIndex folds events into cells and prices each cell once.
func NewIndex(events []model.Event, pt *pricing.Table, loc *time.Location) *Index {
	x := &Index{loc: loc, days: map[int32]string{}}
	at := map[cellKey]int{}
	prices := map[string]*pricing.Price{} // model -> price (nil = unpriced)
	for i := range events {
		e := &events[i]
		dn := dayNum(e.Time, loc)
		k := cellKey{dn, e.Provider, e.Model, e.Project, e.Session}
		ci, ok := at[k]
		if !ok {
			ci = len(x.cells)
			at[k] = ci
			x.cells = append(x.cells, cell{
				day: dn, provider: e.Provider, model: e.Model, project: e.Project, sessKey: e.Provider + "/" + e.Session,
				lowModel: strings.ToLower(e.Model), lowProject: strings.ToLower(e.Project),
				lowHay: strings.ToLower(e.Model + "\x00" + e.Project + "\x00" + e.Session),
				first:  e.Time, last: e.Time,
			})
			if _, ok := x.days[dn]; !ok {
				x.days[dn] = e.Time.In(loc).Format("2006-01-02 Mon")
			}
		}
		c := &x.cells[ci]
		p, seen := prices[e.Model]
		if !seen {
			p = pt.Lookup(e.Model)
			prices[e.Model] = p
		}
		c.priced = p != nil
		if p != nil {
			c.cost += p.Cost(e)
		}
		c.events++
		if e.Tools != "" {
			if c.tools == nil {
				c.tools = map[string]int64{}
			}
			for _, n := range model.ToolNames(e.Tools) {
				c.tools[n]++
			}
		}
		c.input += e.Input
		c.cacheRead += e.CacheRead
		c.cacheWrite += e.CacheWrite
		c.output += e.Output
		c.reasoning += e.Reasoning
		c.has |= e.Has
		if e.Has&model.HasCacheRead != 0 {
			c.shareRead += e.CacheRead
			c.shareIn += e.Input + e.CacheRead + e.CacheWrite
		}
		for j, bit := range [3]uint8{model.HasCacheRead, model.HasCacheWrite, model.HasReasoning} {
			if e.Has&bit != 0 {
				c.hasCount[j]++
			}
		}
		if e.Time.Before(c.first) {
			c.first = e.Time
		}
		if e.Time.After(c.last) {
			c.last = e.Time
		}
	}
	return x
}

// Span is the first and last day that has any usage (zero if none).
func (x *Index) Span() (first, last time.Time) {
	var lo, hi int32
	for i := range x.cells {
		if d := x.cells[i].day; i == 0 || d < lo {
			lo = d
		}
		if d := x.cells[i].day; i == 0 || d > hi {
			hi = d
		}
	}
	if len(x.cells) == 0 {
		return
	}
	at := func(d int32) time.Time {
		u := time.Unix(int64(d)*86400, 0).UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, x.loc)
	}
	return at(lo), at(hi)
}

// Len is the number of cells (useful for tests and diagnostics).
func (x *Index) Len() int { return len(x.cells) }

type cellFilter struct {
	from, to       int32 // [from, to) in day numbers
	hasFrom, hasTo bool
	f              *Filter
	text           string
	models, projs  []string
	// exact names (lower case): a row matches only itself
	modelsExact, projsExact []string
}

func (x *Index) compile(f *Filter) cellFilter {
	cf := cellFilter{f: f, text: strings.ToLower(f.Text)}
	if !f.From.IsZero() {
		cf.from, cf.hasFrom = dayNum(f.From, x.loc), true
	}
	if !f.To.IsZero() {
		cf.to, cf.hasTo = dayNum(f.To, x.loc), true
		if !atDayStart(f.To, x.loc) { // a bound inside a day includes that day
			cf.to++
		}
	}
	for _, m := range f.Models {
		cf.models = append(cf.models, strings.ToLower(m))
	}
	for _, p := range f.Projects {
		cf.projs = append(cf.projs, strings.ToLower(p))
	}
	for _, m := range f.ModelsExact {
		cf.modelsExact = append(cf.modelsExact, strings.ToLower(m))
	}
	for _, p := range f.ProjectsExact {
		cf.projsExact = append(cf.projsExact, strings.ToLower(p))
	}
	return cf
}

func (cf *cellFilter) match(c *cell) bool {
	if cf.hasFrom && c.day < cf.from {
		return false
	}
	if cf.hasTo && c.day >= cf.to {
		return false
	}
	if len(cf.f.Providers) > 0 && !contains(cf.f.Providers, c.provider) {
		return false
	}
	if len(cf.models) > 0 && !anyIn(cf.models, c.lowModel) {
		return false
	}
	if len(cf.projs) > 0 && !anyIn(cf.projs, c.lowProject) {
		return false
	}
	if len(cf.modelsExact) > 0 && !contains(cf.modelsExact, c.lowModel) {
		return false
	}
	if len(cf.projsExact) > 0 && !contains(cf.projsExact, c.lowProject) {
		return false
	}
	if cf.text != "" && !strings.Contains(c.lowHay, cf.text) {
		return false
	}
	return true
}

func anyIn(subs []string, s string) bool {
	for _, v := range subs {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

// Summarize is the one-shot form: index, then summarize.
func Summarize(events []model.Event, f Filter, pt *pricing.Table, loc *time.Location) *Summary {
	return NewIndex(events, pt, loc).Summarize(f)
}

// Summarize groups the cells that pass f. Day buckets use the index's zone.
func (x *Index) Summarize(f Filter) *Summary {
	loc := x.loc
	s := &Summary{Filter: f, Total: newGroup("total", "Total"), Loc: loc}
	day := map[string]*Group{}
	prov, mod, proj, sess := map[string]*Group{}, map[string]*Group{}, map[string]*Group{}, map[string]*Group{}
	tools := map[string]int64{}
	get := func(m map[string]*Group, k, l string) *Group {
		g := m[k]
		if g == nil {
			g = newGroup(k, l)
			m[k] = g
		}
		return g
	}
	cf := x.compile(&f)
	var prevCF cellFilter
	var prevF Filter
	if !f.From.IsZero() && !f.To.IsZero() {
		prevF = f
		prevF.From, prevF.To = PrevWindow(f.From, f.To, x.loc)
		if !f.PrevFrom.IsZero() && !f.PrevTo.IsZero() {
			prevF.From, prevF.To = f.PrevFrom, f.PrevTo
		}
		prevCF = x.compile(&prevF)
		s.Prev = newGroup("prev", "Previous period")
	}
	for i := range x.cells {
		c := &x.cells[i]
		if s.Prev != nil && prevCF.match(c) {
			s.Prev.addCell(c)
		}
		if !cf.match(c) {
			continue
		}
		s.Total.addCell(c)
		dl := x.days[c.day]
		get(day, dl[:10], dl).addCell(c)
		get(prov, c.provider, c.provider).addCell(c)
		get(mod, c.provider+"/"+c.model, c.model).addCell(c)
		get(proj, c.project, ProjectLabel(c.project)).addCell(c)
		get(sess, c.sessKey, strings.TrimPrefix(c.sessKey, c.provider+"/")).addCell(c)
		for name, n := range c.tools {
			tools[name] += n
		}
	}
	for name, n := range tools {
		s.Tools = append(s.Tools, ToolStat{Name: name, Uses: n})
	}
	sort.Slice(s.Tools, func(i, j int) bool {
		if s.Tools[i].Uses != s.Tools[j].Uses {
			return s.Tools[i].Uses > s.Tools[j].Uses
		}
		return s.Tools[i].Name < s.Tools[j].Name
	})
	s.ByDay = fillDays(day, f, loc, time.Now())
	s.ByProvider, s.ByModel, s.ByProject, s.BySession = sorted(prov), sorted(mod), sorted(proj), sorted(sess)
	shortenSessions(s.BySession)
	shortenProjects(s.ByProject)
	disambiguate(s.ByModel)
	for _, g := range s.BySession {
		for p := range g.Providers {
			g.Label = p + " " + g.Label
		}
	}
	return s
}

// disambiguate appends the provider to model names that more than one
// provider reports (gpt-5.4 via Codex and via OpenCode are different rows).
func disambiguate(gs []*Group) {
	seen := map[string]int{}
	for _, g := range gs {
		seen[g.Label]++
	}
	for _, g := range gs {
		if seen[g.Label] > 1 {
			for p := range g.Providers {
				g.Label += " (" + p + ")"
			}
		}
	}
}

func sorted(m map[string]*Group) []*Group {
	out := make([]*Group, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Tokens(), out[j].Tokens(); a != b {
			return a > b
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// fillDays returns one group per local day of the window, zero-usage days
// included, so "N of M days active" counts the whole period. The window is
// [From, To) clipped to today (now). A missing bound falls back to the first
// or last day with usage.
func fillDays(m map[string]*Group, f Filter, loc *time.Location, now time.Time) []*Group {
	bounded := !f.From.IsZero() && !f.To.IsZero()
	if len(m) == 0 && !bounded {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var first, last time.Time
	if !f.From.IsZero() {
		first = civilDay(f.From, loc)
	} else {
		first, _ = time.Parse("2006-01-02", keys[0])
	}
	if !f.To.IsZero() {
		last = civilDay(f.To, loc)
		if atDayStart(f.To, loc) { // To is exclusive
			last = last.AddDate(0, 0, -1)
		}
		if today := civilDay(now, loc); last.After(today) {
			last = today
		}
	} else {
		last, _ = time.Parse("2006-01-02", keys[len(keys)-1])
	}
	var out []*Group
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		if g, ok := m[k]; ok {
			out = append(out, g)
		} else {
			out = append(out, newGroup(k, k+" "+d.Format("Mon")))
		}
	}
	return out
}

// civilDay is the calendar day of t in loc, as a UTC midnight so that
// day arithmetic never depends on daylight-saving offsets.
func civilDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ProjectLabel shortens a working directory to its last two path elements.
func ProjectLabel(p string) string { return projectLabel(p, 2) }

// projectLabel keeps the last n path elements of a working directory.
func projectLabel(p string, n int) string {
	if p == "" {
		return "(unknown)"
	}
	p = strings.Trim(filepath.ToSlash(p), "/")
	parts := strings.Split(p, "/")
	if len(parts) <= n {
		return p
	}
	return strings.Join(parts[len(parts)-n:], "/")
}

// shortenProjects lengthens the project labels until they are unique within
// the summary: "/a/x/app" and "/b/x/app" both read "x/app" by default, so
// each shows as many elements as it takes to tell them apart.
func shortenProjects(gs []*Group) {
	maxParts := 0
	for _, g := range gs {
		maxParts = max(maxParts, strings.Count(strings.Trim(filepath.ToSlash(g.Key), "/"), "/")+1)
	}
	for n := 2; ; n++ {
		labels := make([]string, len(gs))
		seen := map[string]bool{}
		unique := true
		for i, g := range gs {
			labels[i] = projectLabel(g.Key, n)
			unique = unique && !seen[labels[i]]
			seen[labels[i]] = true
		}
		if unique || n >= maxParts {
			for i, g := range gs {
				g.Label = labels[i]
			}
			return
		}
	}
}

// shortIDLen is how much of a session id is shown by default.
const shortIDLen = 8

// ShortID abbreviates a session id to its first runes.
func ShortID(s string) string { return truncRunes(s, shortIDLen) }

// truncRunes keeps the first n runes of s without splitting one.
func truncRunes(s string, n int) string {
	i := 0
	for k := 0; k < n && i < len(s); k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// shortenSessions gives each session group a label that is unique within the
// summary. A UUIDv7 starts with a timestamp, so sessions begun close together
// share a prefix and the label grows until they differ.
func shortenSessions(gs []*Group) {
	raw := make([]string, len(gs))
	maxLen := 0
	for i, g := range gs {
		raw[i] = g.Label
		maxLen = max(maxLen, len(g.Label))
	}
	for n := shortIDLen; ; n++ {
		labels := make([]string, len(raw))
		seen := map[string]bool{}
		unique := true
		for i, r := range raw {
			labels[i] = truncRunes(r, n)
			unique = unique && !seen[labels[i]]
			seen[labels[i]] = true
		}
		if unique || n >= maxLen { // identical full ids cannot be told apart
			for i, g := range gs {
				g.Label = labels[i]
			}
			return
		}
	}
}

// Change describes cur versus prev as a fraction, if comparable.
func Change(cur, prev float64) (float64, bool) {
	if prev <= 0 {
		return 0, false
	}
	return (cur - prev) / prev, true
}

// ProviderCoverage describes which optional fields a provider reported in
// the selected data, so cross-provider comparisons can be labelled.
type ProviderCoverage struct {
	Provider                         string
	Events                           int
	CacheRead, CacheWrite, Reasoning int // events that reported the field
}

// Coverage lists per-provider field coverage.
func (s *Summary) Coverage() []ProviderCoverage {
	var out []ProviderCoverage
	for _, g := range s.ByProvider {
		out = append(out, ProviderCoverage{g.Key, g.Events, g.HasCount[0], g.HasCount[1], g.HasCount[2]})
	}
	return out
}

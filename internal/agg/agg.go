// Package agg turns events into the groupings, comparisons and coverage
// statements shown by the report, the TUI and the exports.
package agg

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
)

// Filter selects events. Empty fields match everything. To is exclusive.
type Filter struct {
	From, To  time.Time
	Providers []string // exact provider ids
	Models    []string // case-insensitive substrings, any may match
	Projects  []string // case-insensitive substrings of the project path
	Text      string   // free text: matches model, project or session (TUI search)
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
	First, Last time.Time
	Sessions    map[string]struct{}
	Models      map[string]struct{}
	Providers   map[string]struct{}
	Unpriced    map[string]int64 // unpriced model -> tokens
}

// Tokens is the provider-neutral total.
func (g *Group) Tokens() int64 { return g.Input + g.CacheRead + g.CacheWrite + g.Output }

// CacheShare is cache-read tokens as a share of all input-side tokens.
func (g *Group) CacheShare() (float64, bool) {
	in := g.Input + g.CacheRead + g.CacheWrite
	if in == 0 {
		return 0, false
	}
	return float64(g.CacheRead) / float64(in), true
}

// CostKnown is false when nothing in the group could be priced.
func (g *Group) CostKnown() bool { return g.PricedTok > 0 || g.Tokens() == 0 }

// Complete is false when some tokens could not be priced.
func (g *Group) Complete() bool { return g.UnpricedTok == 0 }

func newGroup(k, l string) *Group {
	return &Group{Key: k, Label: l, Sessions: map[string]struct{}{}, Models: map[string]struct{}{},
		Providers: map[string]struct{}{}, Unpriced: map[string]int64{}}
}

func (g *Group) add(e *model.Event, pt *pricing.Table) {
	g.Events++
	g.Input += e.Input
	g.CacheRead += e.CacheRead
	g.CacheWrite += e.CacheWrite
	g.Output += e.Output
	g.Reasoning += e.Reasoning
	g.Has |= e.Has
	for i, bit := range [3]uint8{model.HasCacheRead, model.HasCacheWrite, model.HasReasoning} {
		if e.Has&bit != 0 {
			g.HasCount[i]++
		}
	}
	if g.First.IsZero() || e.Time.Before(g.First) {
		g.First = e.Time
	}
	if e.Time.After(g.Last) {
		g.Last = e.Time
	}
	g.Sessions[e.Provider+"/"+e.Session] = struct{}{}
	g.Models[e.Model] = struct{}{}
	g.Providers[e.Provider] = struct{}{}
	if p := pt.Lookup(e.Model); p != nil {
		g.Cost += p.Cost(e)
		g.PricedTok += e.Total()
	} else {
		g.UnpricedTok += e.Total()
		g.Unpriced[e.Model] += e.Total()
	}
}

// Summary is everything a view needs.
type Summary struct {
	Filter     Filter
	Total      *Group
	Prev       *Group // same-length period immediately before, if the range is bounded
	ByDay      []*Group
	ByProvider []*Group
	ByModel    []*Group
	ByProject  []*Group
	BySession  []*Group
	Loc        *time.Location
}

// Summarize groups events that pass f. Day buckets use loc.
func Summarize(events []model.Event, f Filter, pt *pricing.Table, loc *time.Location) *Summary {
	s := &Summary{Filter: f, Total: newGroup("total", "Total"), Loc: loc}
	day := map[string]*Group{}
	prov, mod, proj, sess := map[string]*Group{}, map[string]*Group{}, map[string]*Group{}, map[string]*Group{}
	get := func(m map[string]*Group, k, l string) *Group {
		g := m[k]
		if g == nil {
			g = newGroup(k, l)
			m[k] = g
		}
		return g
	}
	var prevF Filter
	if !f.From.IsZero() && !f.To.IsZero() {
		d := f.To.Sub(f.From)
		prevF = f
		prevF.From, prevF.To = f.From.Add(-d), f.From
		s.Prev = newGroup("prev", "Previous period")
	}
	for i := range events {
		e := &events[i]
		if s.Prev != nil && prevF.Match(e) {
			s.Prev.add(e, pt)
		}
		if !f.Match(e) {
			continue
		}
		s.Total.add(e, pt)
		d := e.Time.In(loc).Format("2006-01-02")
		get(day, d, d).add(e, pt)
		get(prov, e.Provider, e.Provider).add(e, pt)
		get(mod, e.Provider+"/"+e.Model, e.Model).add(e, pt)
		get(proj, e.Project, ProjectLabel(e.Project)).add(e, pt)
		get(sess, e.Provider+"/"+e.Session, ShortID(e.Session)).add(e, pt)
	}
	s.ByDay = fillDays(day, f, loc)
	s.ByProvider, s.ByModel, s.ByProject, s.BySession = sorted(prov), sorted(mod), sorted(proj), sorted(sess)
	for _, g := range s.BySession {
		for p := range g.Providers {
			g.Label = p + " " + g.Label
		}
	}
	return s
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

// fillDays returns days in order, with zero-usage days filled in between the
// first and last day so trends are not visually compressed.
func fillDays(m map[string]*Group, f Filter, loc *time.Location) []*Group {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first, _ := time.ParseInLocation("2006-01-02", keys[0], loc)
	last, _ := time.ParseInLocation("2006-01-02", keys[len(keys)-1], loc)
	if !f.From.IsZero() && f.From.In(loc).After(first) {
		first = time.Date(f.From.In(loc).Year(), f.From.In(loc).Month(), f.From.In(loc).Day(), 0, 0, 0, 0, loc)
	}
	var out []*Group
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		if g, ok := m[k]; ok {
			out = append(out, g)
		} else {
			out = append(out, newGroup(k, k))
		}
	}
	return out
}

// ProjectLabel shortens a working directory to its last two path elements.
func ProjectLabel(p string) string {
	if p == "" {
		return "(unknown)"
	}
	p = filepath.ToSlash(p)
	p = strings.TrimRight(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// ShortID abbreviates a session id.
func ShortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
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

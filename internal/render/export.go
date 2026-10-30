package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
)

// Dimensions that can be exported.
var Dimensions = []string{"day", "provider", "model", "project", "session", "events"}

type jsonGroup struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Sessions   int      `json:"sessions"`
	Calls      int      `json:"calls"`
	Input      int64    `json:"input_tokens"`
	CacheRead  *int64   `json:"cache_read_tokens"`  // null = not reported by the source
	CacheWrite *int64   `json:"cache_write_tokens"` // null = not reported by the source
	Output     int64    `json:"output_tokens"`
	Reasoning  *int64   `json:"reasoning_tokens"` // subset of output; null = not reported
	Total      int64    `json:"total_tokens"`
	CostUSD    *float64 `json:"cost_usd"`    // null = unpriced; see cost_status
	CostStatus string   `json:"cost_status"` // priced | partial | unpriced | none
	Unpriced   []string `json:"unpriced_models,omitempty"`
}

func costStatus(g *agg.Group) string {
	switch {
	case g.Tokens() == 0:
		return "none"
	case g.PricedTok == 0:
		return "unpriced"
	case !g.Complete():
		return "partial" // cost_usd is a lower bound
	}
	return "priced"
}

func toJSON(g *agg.Group) jsonGroup {
	j := jsonGroup{Key: g.Key, Label: g.Label, Sessions: len(g.Sessions), Calls: g.Events,
		Input: g.Input, Output: g.Output, Total: g.Tokens(), CostStatus: costStatus(g)}
	opt := func(bit uint8, v int64) *int64 {
		if g.Has&bit == 0 {
			return nil
		}
		return &v
	}
	j.CacheRead, j.CacheWrite, j.Reasoning = opt(model.HasCacheRead, g.CacheRead), opt(model.HasCacheWrite, g.CacheWrite), opt(model.HasReasoning, g.Reasoning)
	if g.PricedTok > 0 {
		c := g.Cost
		j.CostUSD = &c
	}
	for m := range g.Unpriced {
		j.Unpriced = append(j.Unpriced, m)
	}
	sort.Strings(j.Unpriced)
	return j
}

func list(gs []*agg.Group) []jsonGroup {
	out := make([]jsonGroup, 0, len(gs))
	for _, g := range gs {
		out = append(out, toJSON(g))
	}
	return out
}

// JSON writes the whole summary as one document.
func JSON(w io.Writer, c *Ctx) error {
	s := c.Sum
	type src struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Files  int    `json:"files"`
		Detail string `json:"detail"`
	}
	var sources []src
	for _, id := range provOrder {
		if d, ok := c.Detect[id]; ok {
			sources = append(sources, src{id, provName(id), d.Status.String(), c.Files[id], d.Detail})
		}
	}
	doc := map[string]any{
		"generated":   time.Now().UTC().Format(time.RFC3339),
		"range":       map[string]any{"name": c.RangeName, "from": ts(s.Filter.From), "to_exclusive": ts(s.Filter.To), "timezone": zoneName(s.Loc), "utc_offset": utcOffset(s.Filter.From, s.Loc)},
		"filters":     c.Filters,
		"total":       toJSON(s.Total),
		"by_day":      list(s.ByDay),
		"by_provider": list(s.ByProvider),
		"by_model":    list(s.ByModel),
		"by_project":  list(s.ByProject),
		"by_session":  list(s.BySession),
		"sources":     sources,
		"parse":       map[string]int{"malformed": c.Stats.Malformed, "clamped": c.Stats.Anomalies, "oversized": c.Stats.Oversized, "duplicates_merged": c.Duplicate, "unreadable_files": c.Errors},
		"notes": []string{
			"cost_usd is an API-equivalent estimate at list prices, not a subscription bill",
			"null token fields mean the source did not report them; they are not zero",
			"total_tokens = input + cache_read + cache_write + output (reasoning is a subset of output)",
		},
		"prices": priceSources(c.Prices),
	}
	if s.Prev != nil {
		doc["previous_period"] = toJSON(s.Prev)
	}
	doc["partial"] = c.Cancelled // a cancelled scan has read only some files
	if c.Cancelled {
		doc["partial_reason"] = "scan cancelled: totals cover only the files read before cancelling"
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// zoneName is the IANA name of loc, or nil when only "Local" is known. Go
// names time.Local after TZ when TZ is a zone name; otherwise the zone is
// just the system's local time and its name is not known.
func zoneName(loc *time.Location) any {
	if n := loc.String(); n != "Local" {
		return n
	}
	return nil
}

// utcOffset is the offset of loc at t (the period start; now when unbounded), as +hh:mm.
func utcOffset(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		t = time.Now()
	}
	_, off := t.In(loc).Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("%s%02d:%02d", sign, off/3600, off%3600/60)
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func priceSources(p *pricing.Table) any {
	if p == nil {
		return nil
	}
	return p.Sources
}

// csvSafe neutralises spreadsheet formula injection: a cell that starts
// with = + - @ (e.g. a hostile project directory name) is prefixed with '.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

var csvHead = []string{"key", "label", "sessions", "calls", "input_tokens", "cache_read_tokens", "cache_write_tokens",
	"output_tokens", "reasoning_tokens", "total_tokens", "cost_usd", "cost_status"}

// CSV writes one dimension. Unreported fields and unpriced costs are empty
// cells, never zeros.
func CSV(w io.Writer, c *Ctx, by string, events []model.Event, f *agg.Filter) error {
	cw := csv.NewWriter(w)
	if by == "events" {
		_ = cw.Write([]string{"time", "provider", "model", "session", "project", "input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "reasoning_tokens", "total_tokens", "cost_usd"})
		for i := range events {
			e := &events[i]
			if !f.Match(e) {
				continue
			}
			cost := ""
			if p := c.Prices.Lookup(e.Model); p != nil {
				cost = strconv.FormatFloat(p.Cost(e), 'f', 6, 64)
			}
			opt := func(bit uint8, v int64) string {
				if e.Has&bit == 0 {
					return ""
				}
				return strconv.FormatInt(v, 10)
			}
			_ = cw.Write([]string{e.Time.UTC().Format(time.RFC3339), e.Provider, csvSafe(e.Model), csvSafe(e.Session), csvSafe(e.Project),
				strconv.FormatInt(e.Input, 10), opt(model.HasCacheRead, e.CacheRead), opt(model.HasCacheWrite, e.CacheWrite),
				strconv.FormatInt(e.Output, 10), opt(model.HasReasoning, e.Reasoning), strconv.FormatInt(e.Total(), 10), cost})
		}
		cw.Flush() // the write errors only surface here
		return cw.Error()
	}
	var gs []*agg.Group
	switch by {
	case "day":
		gs = c.Sum.ByDay
	case "provider":
		gs = c.Sum.ByProvider
	case "model":
		gs = c.Sum.ByModel
	case "project":
		gs = c.Sum.ByProject
	case "session":
		gs = c.Sum.BySession
	default:
		return fmt.Errorf("unknown dimension %q (use one of: %s)", by, strings.Join(Dimensions, ", "))
	}
	_ = cw.Write(csvHead)
	for _, g := range gs {
		j := toJSON(g)
		o := func(p *int64) string {
			if p == nil {
				return ""
			}
			return strconv.FormatInt(*p, 10)
		}
		cost := ""
		if j.CostUSD != nil {
			cost = strconv.FormatFloat(*j.CostUSD, 'f', 6, 64)
		}
		_ = cw.Write([]string{csvSafe(j.Key), csvSafe(j.Label), strconv.Itoa(j.Sessions), strconv.Itoa(j.Calls),
			strconv.FormatInt(j.Input, 10), o(j.CacheRead), o(j.CacheWrite), strconv.FormatInt(j.Output, 10), o(j.Reasoning),
			strconv.FormatInt(j.Total, 10), cost, j.CostStatus})
	}
	cw.Flush() // the write errors only surface here
	return cw.Error()
}

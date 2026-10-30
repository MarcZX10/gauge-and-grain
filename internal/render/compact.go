package render

import (
	"fmt"
	"strings"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Compact is the tiny summary: the few numbers that matter and nothing
// else — usage, estimated cost, sessions and cache use with their change
// versus the previous period, the top items, a one-line trend, and a pointer
// to the notes only when something needs attention. It is the same data as
// the full overview, so the two can never disagree.
func Compact(t Theme, c *Ctx, width int) []string {
	s := c.Sum
	tot := s.Total
	if tot.Tokens() == 0 {
		return []string{t.Dim("no usage in this range")}
	}
	sep := t.Dim("  ·  ")

	// 1. the headline
	tok := t.FG(CAccent, t.Bold(Tokens(tot.Tokens()))) + t.Dim(" tokens")
	if s.Prev != nil {
		if ch, ok := agg.Change(float64(tot.Tokens()), float64(s.Prev.Tokens())); ok {
			tok += " " + t.Arrow(ch)
		}
	}
	var cost string
	switch {
	case tot.PricedTok == 0:
		cost = t.Warn(t.Bold("unpriced"))
	case !tot.Complete():
		cost = t.Warn(t.Bold("≥ "+Cost(tot.Cost))) + t.Dim(" est.")
	default:
		cost = t.FG(CGood, t.Bold(Cost(tot.Cost))) + t.Dim(" est.")
		if s.Prev != nil {
			if ch, ok := agg.Change(tot.Cost, s.Prev.Cost); ok && s.Prev.Complete() {
				cost += " " + t.Arrow(ch)
			} else if !s.Prev.Complete() {
				cost += " " + t.Dim("vs prev n/a")
			}
		}
	}
	parts := []string{tok, cost, t.Bold(Num(len(tot.Sessions))) + t.Dim(" sessions")}
	if sh, ok := tot.CacheShare(); ok && tot.Has&model.HasCacheRead != 0 {
		parts = append(parts, t.Bold(Pct(sh))+t.Dim(" cached"))
	}
	out := []string{strings.Join(parts, sep)}

	// 2. the leaders
	if width >= 56 {
		var top []string
		add := func(label string, gs []*agg.Group) {
			if len(gs) > 0 {
				top = append(top, t.Dim(label+" ")+gs[0].Label+t.Dim(" "+Pct(float64(gs[0].Tokens())/float64(tot.Tokens()))))
			}
		}
		if len(s.ByProvider) > 1 {
			add("top provider", s.ByProvider)
		}
		add("model", s.ByModel)
		add("project", s.ByProject)
		if len(top) > 0 {
			out = append(out, strings.Join(top, sep))
		}
	}

	// 3. the trend, in one line
	if len(s.ByDay) > 1 && width >= 40 {
		vals := make([]float64, len(s.ByDay))
		peak := s.ByDay[0]
		for i, d := range s.ByDay {
			vals[i] = float64(d.Tokens())
			if d.Tokens() > peak.Tokens() {
				peak = d
			}
		}
		spark := t.Spark(Bucket(vals, clampInt(width-34, 8, 48)))
		out = append(out, t.FG(CAccent, spark)+t.Dim("  peak "+peak.Key[5:10]+" "+Tokens(peak.Tokens())))
	}

	// 4. only if something needs attention
	if warn, _ := NoteCounts(c); warn > 0 {
		verb := "need"
		if warn == 1 {
			verb = "needs"
		}
		out = append(out, t.Warn(fmt.Sprintf("⚠ %d %s %s attention", warn, noun(warn, "note"), verb))+t.Dim(" · gg notes"))
	}
	return out
}

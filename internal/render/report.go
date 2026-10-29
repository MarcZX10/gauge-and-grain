package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// Ctx is the context a view needs besides the summary.
type Ctx struct {
	Sum       *agg.Summary
	Detect    map[string]providers.Detection
	Files     map[string]int
	Stats     model.ParseStats
	Cached    int
	Parsed    int
	Duplicate int
	Errors    int
	Cancelled bool
	Prices    *pricing.Table
	RangeName string // e.g. "last 30 days"
	Filters   string // human description of non-date filters
}

var provOrder = []string{"claude", "codex", "gemini"}

func provName(id string) string {
	if p := providers.ByID(id); p != nil {
		return p.Name()
	}
	return id
}

// Section renders a heading line.
func (t Theme) Section(title, hint string) string {
	h := t.Head(strings.ToUpper(title))
	if hint != "" {
		h += "  " + t.Dim(hint)
	}
	return h
}

// Header is the title + selected range.
func Header(t Theme, c *Ctx, width int) []string {
	f := c.Sum.Filter
	rng := c.RangeName
	if !f.From.IsZero() || !f.To.IsZero() {
		loc := c.Sum.Loc
		from, to := "…", "now"
		if !f.From.IsZero() {
			from = f.From.In(loc).Format("2006-01-02")
		}
		if !f.To.IsZero() {
			to = f.To.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
		}
		rng = fmt.Sprintf("%s  (%s → %s)", c.RangeName, from, to)
	}
	l := t.Bold("gauge-and-grain") + "  " + rng
	if c.Filters != "" {
		l += "  " + t.Warn("filter: "+c.Filters)
	}
	return []string{l}
}

// Overview is the headline block: tokens, cost, sessions, sources.
func Overview(t Theme, c *Ctx, width int) []string {
	s := c.Sum
	tot := s.Total
	var out []string
	row := func(label, val, note string) {
		l := t.Dim(t.Pad(label, 10)) + t.Bold(val)
		if note != "" {
			l += strings.Repeat(" ", max(2, 14-Width(val))) + note
		}
		out = append(out, l)
	}
	tokNote := t.Dim("input + cache + output, all providers normalised the same way")
	if s.Prev != nil {
		if ch, ok := agg.Change(float64(tot.Tokens()), float64(s.Prev.Tokens())); ok {
			tokNote = t.Arrow(ch) + t.Dim(fmt.Sprintf(" vs previous period (%s)", Tokens(s.Prev.Tokens())))
		} else if tot.Tokens() > 0 {
			tokNote = t.Dim("no usage in the previous period to compare with")
		}
	}
	row("Tokens", Tokens(tot.Tokens()), tokNote)

	costVal, costNote := "unpriced", t.Warn("no model in this range has a known price (see `gg prices`)")
	if tot.CostKnown() && tot.PricedTok > 0 {
		costVal = Cost(tot.Cost)
		if !tot.Complete() {
			costVal = "≥ " + Cost(tot.Cost)
			if t.ASCII {
				costVal = ">= " + Cost(tot.Cost)
			}
			costNote = t.Warn(fmt.Sprintf("partial: %s tokens unpriced (%s)", Tokens(tot.UnpricedTok), plural(len(tot.Unpriced), "model")))
		} else {
			costNote = t.Dim("API-equivalent estimate at list prices; not your subscription bill")
		}
		if s.Prev != nil {
			if ch, ok := agg.Change(tot.Cost, s.Prev.Cost); ok && tot.Complete() {
				costNote = t.Arrow(ch) + t.Dim(fmt.Sprintf(" vs previous (%s) · API-equivalent estimate", Cost(s.Prev.Cost)))
			}
		}
	} else if tot.Tokens() == 0 {
		costVal, costNote = "$0.00", ""
	}
	row("Est. cost", costVal, costNote)
	row("Sessions", Num(len(tot.Sessions)), t.Dim(fmt.Sprintf("%s API calls (de-duplicated)", Num(tot.Events))))

	var srcs []string
	for _, id := range provOrder {
		d, ok := c.Detect[id]
		if !ok {
			continue
		}
		switch {
		case hasProv(tot, id):
			srcs = append(srcs, provName(id))
		case d.Status == providers.Unsupported:
			srcs = append(srcs, t.Warn(provName(id)+" (found, not parsed)"))
		case d.Status == providers.Ready:
			srcs = append(srcs, t.Dim(provName(id)+" (no usage in range)"))
		default:
			srcs = append(srcs, t.Dim(provName(id)+" (not found)"))
		}
	}
	row("Sources", strings.Join(srcs, " · "), "")
	return out
}

func hasProv(g *agg.Group, id string) bool { _, ok := g.Providers[id]; return ok }

// TokenMix shows where tokens went: input, cache read/write, output.
func TokenMix(t Theme, c *Ctx, width int) []string {
	g := c.Sum.Total
	out := []string{t.Section("Token mix", "what each kind of token means")}
	tot := float64(g.Tokens())
	if tot == 0 {
		return append(out, t.Dim("  no usage in this range"))
	}
	bw := clampInt(width-84, 6, 20)
	reported := func(bit uint8) bool { return g.Has&bit != 0 }
	line := func(name string, n int64, ok bool, note string) {
		if !ok {
			out = append(out, "  "+t.Pad(name, 20)+t.PadL("n/a", 9)+"  "+t.Dim("not reported by any selected source"))
			return
		}
		out = append(out, "  "+t.Pad(name, 20)+t.PadL(Tokens(n), 9)+"  "+t.PadL(Pct(float64(n)/tot), 6)+"  "+t.Bar(float64(n)/tot, bw)+"  "+t.Dim(note))
	}
	line("input", g.Input, true, "new, uncached prompt tokens")
	line("cache read", g.CacheRead, reported(model.HasCacheRead), "prompt tokens reused from cache (cheap)")
	line("cache write", g.CacheWrite, reported(model.HasCacheWrite), "stored into cache; priced above input")
	line("output", g.Output, true, "generated tokens")
	if reported(model.HasReasoning) {
		out = append(out, "  "+t.Dim(t.Pad("  of which thinking", 20))+t.Dim(t.PadL(Tokens(g.Reasoning), 9))+"  "+t.Dim("reasoning tokens: a subset of output, not added again"))
	}
	if sh, ok := g.CacheShare(); ok && reported(model.HasCacheRead) {
		out = append(out, "  "+t.Dim(fmt.Sprintf("cache hit share of input-side tokens: %s", Pct(sh))))
	}
	return out
}

// TableRows renders a ranked table: one header line and one line per group.
// Columns drop away on narrow widths; nothing ever exceeds width.
func TableRows(t Theme, gs []*agg.Group, total int64, width int, labelHdr string, flagUnpriced bool) (string, []string) {
	showCost := width >= 56
	showBar := width >= 78
	costW, tokW, sesW := 10, 9, 5
	barW := 14
	fixed := 2 + tokW + 2 + 6 + 2
	if showCost {
		fixed += costW + 2
	}
	fixed += sesW + 2
	if showBar {
		fixed += barW + 2
	}
	lw := clampInt(width-fixed, 6, 44)
	hdr := "  " + t.Dim(t.Pad(labelHdr, lw)+"  "+t.PadL("tokens", tokW)+"  "+t.PadL("share", 6))
	if showCost {
		hdr += t.Dim("  " + t.PadL("est. cost", costW))
	}
	hdr += t.Dim("  " + t.PadL("sess", sesW))
	rows := make([]string, 0, len(gs))
	for _, g := range gs {
		share := 0.0
		if total > 0 {
			share = float64(g.Tokens()) / float64(total)
		}
		label := g.Label
		if flagUnpriced && !g.Complete() && lw > 14 {
			flag := " unpriced"
			if g.PricedTok > 0 {
				flag = " partial"
			}
			label = t.Pad(label, lw-len(flag)) + t.Warn(flag)
		}
		l := "  " + t.Pad(label, lw) + "  " + t.PadL(Tokens(g.Tokens()), tokW) + "  " + t.PadL(Pct(share), 6)
		if showCost {
			l += "  " + t.PadL(costCell(t, g), costW)
		}
		l += "  " + t.PadL(Num(len(g.Sessions)), sesW)
		if showBar {
			l += "  " + t.Bar(share, barW)
		}
		rows = append(rows, l)
	}
	return hdr, rows
}

// groupTable renders a ranked table of groups, optionally limited to the top rows.
func groupTable(t Theme, gs []*agg.Group, total int64, width, limit int, labelHdr string, flagUnpriced bool) []string {
	if len(gs) == 0 {
		return []string{t.Dim("  no data")}
	}
	shown := gs
	if limit > 0 && len(gs) > limit {
		shown = gs[:limit]
	}
	hdr, rows := TableRows(t, shown, total, width, labelHdr, flagUnpriced)
	out := append([]string{hdr}, rows...)
	if len(gs) > len(shown) {
		var rest int64
		for _, g := range gs[len(shown):] {
			rest += g.Tokens()
		}
		out = append(out, t.Dim(fmt.Sprintf("  … %d more (%s tokens) — use `gg export` for the full list", len(gs)-len(shown), Tokens(rest))))
	}
	return out
}

func costCell(t Theme, g *agg.Group) string {
	switch {
	case g.Tokens() == 0:
		return "—"
	case g.PricedTok == 0:
		return t.Warn("unpriced")
	case !g.Complete():
		return t.Warn("≥ " + Cost(g.Cost))
	default:
		return Cost(g.Cost)
	}
}

// Providers lists usage per provider with coverage.
func Providers(t Theme, c *Ctx, width, limit int) []string {
	out := []string{t.Section("Providers", "same token definition for all; fields a source lacks are shown as n/a")}
	out = append(out, groupTable(t, c.Sum.ByProvider, c.Sum.Total.Tokens(), width, limit, "provider", true)...)
	return out
}

// Models lists usage per model, flagging unpriced ones.
func Models(t Theme, c *Ctx, width, limit int) []string {
	out := []string{t.Section("Models", "unpriced = no price known; its tokens are counted but cost is excluded")}
	out = append(out, groupTable(t, c.Sum.ByModel, c.Sum.Total.Tokens(), width, limit, "model", true)...)
	return out
}

// Projects lists usage per project.
func Projects(t Theme, c *Ctx, width, limit int) []string {
	out := []string{t.Section("Projects", "working directory the session ran in")}
	out = append(out, groupTable(t, c.Sum.ByProject, c.Sum.Total.Tokens(), width, limit, "project", true)...)
	return out
}

// Sessions lists the heaviest sessions.
func Sessions(t Theme, c *Ctx, width, limit int) []string {
	out := []string{t.Section("Sessions", "heaviest first")}
	out = append(out, groupTable(t, c.Sum.BySession, c.Sum.Total.Tokens(), width, limit, "session", true)...)
	return out
}

// Daily shows the trend as a sparkline plus peak and half-over-half change.
func Daily(t Theme, c *Ctx, width int) []string {
	days := c.Sum.ByDay
	out := []string{t.Section("Daily trend", "tokens per day")}
	if len(days) == 0 {
		return append(out, t.Dim("  no usage in this range"))
	}
	vals := make([]float64, len(days))
	peak := days[0]
	for i, d := range days {
		vals[i] = float64(d.Tokens())
		if d.Tokens() > peak.Tokens() {
			peak = d
		}
	}
	sw := clampInt(width-4, 10, 90)
	out = append(out, "  "+t.Head(t.Spark(Bucket(vals, sw))))
	out = append(out, "  "+t.Dim(fmt.Sprintf("%s %s %s", days[0].Key, strings.Repeat("─", clampInt(width-30, 4, 40)), days[len(days)-1].Key)))
	var active int
	for _, d := range days {
		if d.Tokens() > 0 {
			active++
		}
	}
	out = append(out, "  "+t.Dim(fmt.Sprintf("peak %s (%s)  ·  %d of %d days active  ·  avg %s/active day",
		peak.Key, Tokens(peak.Tokens()), active, len(days), Tokens(c.Sum.Total.Tokens()/int64(max(active, 1))))))
	if len(days) >= 7 {
		h := len(days) / 2
		var a, b float64
		for _, v := range vals[:h] {
			a += v
		}
		for _, v := range vals[len(vals)-h:] {
			b += v
		}
		if ch, ok := agg.Change(b, a); ok {
			out = append(out, "  "+t.Dim("second half vs first half of range: ")+t.Arrow(ch))
		}
	}
	return out
}

// DailyTable lists every day.
func DailyTable(t Theme, c *Ctx, width int) []string {
	out := []string{t.Section("Daily", "")}
	out = append(out, groupTable(t, reverse(c.Sum.ByDay), c.Sum.Total.Tokens(), width, 0, "day", true)...)
	return out
}

func reverse(g []*agg.Group) []*agg.Group {
	out := make([]*agg.Group, len(g))
	for i, v := range g {
		out[len(g)-1-i] = v
	}
	return out
}

// Notes states estimates, gaps and unsupported data. Never empty: the
// cost caveat is always present.
func Notes(t Theme, c *Ctx, width int) []string {
	out := []string{t.Section("Reading this honestly", "")}
	add := func(s string) { out = append(out, wrap(t, "  • ", s, width)...) }
	add("Cost is an estimate of what these tokens would cost at public API list prices — not your subscription bill. Plans like Claude Pro/Max or ChatGPT bill differently, so it will not match an invoice.")
	tot := c.Sum.Total
	if len(tot.Unpriced) > 0 {
		var names []string
		for m := range tot.Unpriced {
			names = append(names, m)
		}
		sort.Slice(names, func(i, j int) bool { return tot.Unpriced[names[i]] > tot.Unpriced[names[j]] })
		if len(names) > 4 {
			names = append(names[:4], fmt.Sprintf("+%d more", len(names)-4))
		}
		add(t.Warn("Unpriced: ") + strings.Join(names, ", ") + ". Their tokens are included above, their cost is not. Add prices in your prices.json (see `gg prices`).")
	}
	for _, cv := range c.Sum.Coverage() {
		var miss []string
		if cv.CacheRead < cv.Events {
			miss = append(miss, "cache reads")
		}
		if cv.CacheWrite < cv.Events {
			miss = append(miss, "cache writes")
		}
		if cv.Reasoning < cv.Events {
			miss = append(miss, "reasoning tokens")
		}
		if len(miss) > 0 {
			add(fmt.Sprintf("%s does not always report %s; shown as part of other counts or n/a, not as invented zeros.", provName(cv.Provider), strings.Join(miss, ", ")))
		}
	}
	for _, id := range provOrder {
		if d, ok := c.Detect[id]; ok && d.Status == providers.Unsupported {
			add(t.Warn(provName(id)+": ") + d.Detail + ".")
		}
	}
	if c.Stats.Malformed+c.Stats.Anomalies+c.Stats.Oversized > 0 {
		add(fmt.Sprintf("Ignored %d malformed record(s), clamped %d implausible counter(s), skipped %d oversized line(s). Totals exclude them.", c.Stats.Malformed, c.Stats.Anomalies, c.Stats.Oversized))
	}
	if c.Duplicate > 0 {
		add(fmt.Sprintf("%s repeated %s (streamed chunks, resumed or archived sessions) counted once.", Num(c.Duplicate), noun(c.Duplicate, "record")))
	}
	if c.Errors > 0 {
		add(t.Warn(fmt.Sprintf("%d file(s) could not be read; their usage is missing.", c.Errors)))
	}
	if c.Cancelled {
		add(t.Warn("Scan was cancelled: totals are partial. Run again to resume from the cache."))
	}
	return out
}

func wrap(t Theme, prefix, s string, width int) []string {
	w := clampInt(width-Width(prefix), 20, 100)
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		if cur != "" && Width(cur)+1+Width(word) > w {
			lines = append(lines, cur)
			cur = word
			continue
		}
		if cur == "" {
			cur = word
		} else {
			cur += " " + word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	for i := range lines {
		pre := prefix
		if i > 0 {
			pre = strings.Repeat(" ", Width(prefix))
		}
		lines[i] = t.Dim(pre) + lines[i]
	}
	return lines
}

func noun(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

func plural(n int, w string) string { return fmt.Sprintf("%d %s", n, noun(n, w)) }

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

// Wrap word-wraps s with a hanging prefix.
func Wrap(t Theme, prefix, s string, width int) []string { return wrap(t, prefix, s, width) }

// Report assembles the compact report.
func Report(t Theme, c *Ctx, width int, daily bool) string {
	if width <= 0 {
		width = 100
	}
	width = min(width, 110)
	var secs [][]string
	secs = append(secs, Header(t, c, width), Overview(t, c, width))
	if c.Sum.Total.Tokens() > 0 {
		secs = append(secs, TokenMix(t, c, width), Daily(t, c, width))
		if daily {
			secs = append(secs, DailyTable(t, c, width))
		}
		secs = append(secs, Providers(t, c, width, 6), Models(t, c, width, 8), Projects(t, c, width, 8))
	}
	secs = append(secs, Notes(t, c, width))
	var b strings.Builder
	for i, s := range secs {
		if i > 0 {
			b.WriteString("\n")
		}
		for _, l := range s {
			b.WriteString(t.Trunc(l, width) + "\n") // never overflow a narrow terminal
		}
	}
	return b.String()
}

// RangeLabel formats a duration for headings.
func RangeLabel(days int) string {
	if days == 1 {
		return "today"
	}
	return fmt.Sprintf("last %d days", days)
}

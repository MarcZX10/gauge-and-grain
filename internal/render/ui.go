package render

import (
	"fmt"
	"strings"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Palette (xterm-256). Colour is used for meaning: providers and token
// kinds have fixed colours everywhere; yellow means caution.
const (
	CAccent = 75
	CGood   = 78
	CWarn   = 214
	CBad    = 203
	CMuted  = 244
	CSelect = 231 // the selected/hovered bar: neutral and high contrast (yellow is reserved for caution)
	CSelBG  = 24

	CInput      = 110
	CCacheRead  = 78
	CCacheWrite = 179
	COutput     = 175
)

// ProviderColor is the fixed colour of a provider.
func ProviderColor(id string) int {
	switch id {
	case "claude":
		return 209
	case "codex":
		return 111
	case "gemini":
		return 141
	case "qwen":
		return 135
	case "cline":
		return 79
	case "roo":
		return 208
	case "kilo":
		return 220
	case "zoo":
		return 114
	case "pi":
		return 175
	case "deepseek":
		return 69
	case "opencode":
		return 183
	case "kilocli":
		return 227
	}
	return CAccent
}

// FG colours s with an xterm-256 foreground.
func (t Theme) FG(n int, s string) string { return t.wrap(fmt.Sprintf("38;5;%d", n), s) }

// Chip draws a label on a coloured background (or [label] without colour).
func (t Theme) Chip(label string, bg int, bold bool) string {
	if !t.Color {
		return "[" + label + "]"
	}
	b := ""
	if bold {
		b = "1;"
	}
	return t.wrap(fmt.Sprintf("%s38;5;255;48;5;%d", b, bg), " "+label+" ")
}

// Select highlights a whole row, keeping the colours inside it.
func (t Theme) Select(s string, w int) string {
	s = t.Pad(s, w)
	if !t.Color {
		return s
	}
	bg := fmt.Sprintf("\x1b[48;5;%dm", CSelBG)
	return bg + strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+bg) + "\x1b[0m"
}

// Dot is a provider-coloured bullet.
func (t Theme) Dot(provider string) string {
	if !t.Color {
		return " "
	}
	g := "●"
	if t.ASCII {
		g = "*"
	}
	return t.FG(ProviderColor(provider), g)
}

// ColBar draws a bar in a colour.
func (t Theme) ColBar(frac float64, w, color int) string {
	b := t.Bar(frac, w)
	if !t.Color {
		return b
	}
	// colour the filled part, mute the rest
	filled := strings.TrimRight(b, "░.")
	rest := b[len(filled):]
	return t.FG(color, filled) + t.FG(238, rest)
}

// Panel draws a titled, bordered box exactly w columns wide.
func (t Theme) Panel(title string, lines []string, w int, color int) []string {
	return t.PanelS(title, lines, w, color, -1, 0)
}

// PanelS is Panel with a scrollbar thumb on the right border covering
// content rows [sbStart, sbStart+sbLen).
func (t Theme) PanelS(title string, lines []string, w int, color int, sbStart, sbLen int) []string {
	if w < 6 {
		w = 6
	}
	tl, tr, bl, br, h, v := "╭", "╮", "╰", "╯", "─", "│"
	if t.ASCII {
		tl, tr, bl, br, h, v = "+", "+", "+", "+", "-", "|"
	}
	inner := w - 4
	bc := func(s string) string { return t.FG(CMuted+2, s) }
	ttl := ""
	tw := 0
	if title != "" {
		ttl = " " + title + " "
		tw = Width(ttl)
		if tw > w-4 {
			ttl = " " + t.Trunc(title, w-6) + " "
			tw = Width(ttl)
		}
	}
	top := bc(tl+h) + t.wrap("1;38;5;"+fmt.Sprint(color), ttl) + bc(strings.Repeat(h, w-3-tw)+tr)
	if !t.Color {
		top = tl + h + ttl + strings.Repeat(h, w-3-tw) + tr
	}
	out := []string{top}
	for i, l := range lines {
		right := bc(v)
		if sbLen > 0 && i >= sbStart && i < sbStart+sbLen {
			thumb := "┃"
			if t.ASCII {
				thumb = "#"
			}
			right = t.FG(color, thumb)
		}
		out = append(out, bc(v)+" "+t.Pad(l, inner)+" "+right)
	}
	out = append(out, bc(bl+strings.Repeat(h, w-2)+br))
	return out
}

// Side joins blocks horizontally; each block is padded to its width and the
// shorter ones are filled with blank lines.
func (t Theme) Side(widths []int, gap int, blocks ...[]string) []string {
	h := 0
	for _, b := range blocks {
		h = max(h, len(b))
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		var sb strings.Builder
		for j, b := range blocks {
			l := ""
			if i < len(b) {
				l = b[i]
			}
			sb.WriteString(t.Pad(l, widths[j]))
			if j < len(blocks)-1 {
				sb.WriteString(strings.Repeat(" ", gap))
			}
		}
		out[i] = sb.String()
	}
	return out
}

// VChart draws a vertical bar chart w×h that fills the full width. sel (>=0)
// highlights one value.
func (t Theme) VChart(vals []float64, w, h, sel int, color int) []string {
	lines := make([]string, h)
	if len(vals) == 0 || w < 1 || h < 1 {
		return lines
	}
	if len(vals) > w {
		vals = Bucket(vals, w)
		sel = -1
	}
	n := len(vals)
	var mx float64
	for _, v := range vals {
		mx = max(mx, v)
	}
	levels := []rune(" ▁▂▃▄▅▆▇█")
	for row := 0; row < h; row++ { // row 0 is the top
		var sb strings.Builder
		for i, v := range vals {
			x0, x1 := i*w/n, (i+1)*w/n // value i owns columns [x0, x1)
			span := x1 - x0
			bw := span
			if span > 1 {
				bw = span - 1 // one-cell gap between bars when there is room
			}
			eighths := 0
			if mx > 0 && v > 0 {
				eighths = int(v / mx * float64(h*8))
				if eighths == 0 {
					eighths = 1
				}
			}
			fill := max(0, min(8, eighths-(h-1-row)*8))
			cell := string(levels[fill])
			if t.ASCII {
				cell = " "
				if fill >= 4 {
					cell = "#"
				} else if fill > 0 {
					cell = "."
				}
			}
			c := color
			if i == sel {
				c = CSelect
			}
			sb.WriteString(t.FG(c, strings.Repeat(cell, bw)) + strings.Repeat(" ", span-bw))
		}
		lines[row] = sb.String()
	}
	return lines
}

// Part is one slice of a stacked bar.
type Part struct {
	Val   float64
	Color int
}

// Stacked draws proportional segments in w cells; non-zero parts get at least one cell.
func (t Theme) Stacked(parts []Part, w int) string {
	var tot float64
	for _, p := range parts {
		tot += p.Val
	}
	if tot <= 0 || w < 1 {
		return strings.Repeat("░", max(0, w))
	}
	cells := make([]int, len(parts))
	used := 0
	for i, p := range parts {
		if p.Val > 0 {
			cells[i] = max(1, int(p.Val/tot*float64(w)))
			used += cells[i]
		}
	}
	for used > w { // shrink the largest until it fits
		bi := 0
		for i := range cells {
			if cells[i] > cells[bi] {
				bi = i
			}
		}
		cells[bi]--
		used--
	}
	for used < w { // give the remainder to the largest
		bi := 0
		for i := range cells {
			if parts[i].Val > parts[bi].Val {
				bi = i
			}
		}
		cells[bi]++
		used++
	}
	var sb strings.Builder
	for i, p := range parts {
		ch := "█"
		if t.ASCII {
			ch = string("#=+-"[i%4])
		}
		sb.WriteString(t.FG(p.Color, strings.Repeat(ch, cells[i])))
	}
	return sb.String()
}

// Detail describes one group: composition, cost, coverage.
func Detail(t Theme, g *agg.Group, w int) []string {
	tot := float64(g.Tokens())
	var out []string
	out = append(out, t.Bold(t.Trunc(g.Label, w)))
	out = append(out, fmt.Sprintf("%s tokens · %s calls · %s sessions", t.Bold(Tokens(g.Tokens())), Num(g.Events), Num(len(g.Sessions))))
	if tot > 0 {
		out = append(out, t.Stacked([]Part{{float64(g.Input), CInput}, {float64(g.CacheRead), CCacheRead}, {float64(g.CacheWrite), CCacheWrite}, {float64(g.Output), COutput}}, w))
		kind := func(name string, color int, n int64, ok bool) string {
			if !ok {
				return t.FG(color, "■ ") + t.Dim(name+" n/a")
			}
			return t.FG(color, "■ ") + name + " " + t.Bold(Tokens(n))
		}
		out = append(out,
			kind("input", CInput, g.Input, true)+"   "+kind("cache read", CCacheRead, g.CacheRead, g.Has&model.HasCacheRead != 0),
			kind("cache write", CCacheWrite, g.CacheWrite, g.Has&model.HasCacheWrite != 0)+"   "+kind("output", COutput, g.Output, true))
		if g.Has&model.HasReasoning != 0 {
			out = append(out, t.Dim(fmt.Sprintf("  of output, %s is reasoning", Tokens(g.Reasoning))))
		}
	}
	switch {
	case g.Tokens() == 0:
	case g.PricedTok == 0:
		out = append(out, t.Warn("cost: unpriced")+t.Dim(" — no known price"))
	case !g.Complete():
		out = append(out, "cost: "+t.Warn("≥ "+Cost(g.Cost))+t.Dim(" (partial)"))
	default:
		out = append(out, "cost: "+t.Bold(Cost(g.Cost))+t.Dim(" API-equivalent"))
	}
	for m, n := range g.Unpriced {
		out = append(out, t.Warn("unpriced ")+m+t.Dim(" "+Tokens(n)))
	}
	if sh, ok := g.CacheShare(); ok && g.Has&model.HasCacheRead != 0 {
		out = append(out, t.Dim("cache hit share "+Pct(sh)))
	}
	if !g.First.IsZero() {
		out = append(out, t.Dim(g.First.Local().Format("2006-01-02")+" → "+g.Last.Local().Format("2006-01-02")))
	}
	if len(g.Models) > 1 {
		out = append(out, t.Dim(fmt.Sprintf("%d models", len(g.Models))))
	}
	return out
}

// ProviderColorOr is the provider colour for a single-provider group, else the accent.
func ProviderColorOr(g *agg.Group) int {
	if len(g.Providers) == 1 {
		for p := range g.Providers {
			return ProviderColor(p)
		}
	}
	return CAccent
}

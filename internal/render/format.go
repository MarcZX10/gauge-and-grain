// Package render draws the compact report and the pieces the TUI reuses.
package render

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/mattn/go-runewidth"
)

// Theme controls colour and glyphs. Colour carries meaning only: yellow =
// caution (estimates, unpriced, partial), cyan = headings, dim = explanations.
// Direction of change is shown by the arrow, never by colour.
type Theme struct {
	Color bool
	ASCII bool
}

func (t Theme) wrap(code, s string) string {
	if !t.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (t Theme) Bold(s string) string   { return t.wrap("1", s) }
func (t Theme) Dim(s string) string    { return t.wrap("2", s) }
func (t Theme) Head(s string) string   { return t.wrap("1;36", s) }
func (t Theme) Warn(s string) string   { return t.wrap("33", s) }
func (t Theme) Up(s string) string     { return s } // direction is the arrow's job
func (t Theme) Down(s string) string   { return s }
func (t Theme) Invert(s string) string { return t.wrap("7", s) }

// asciiGlyphs maps every glyph the interface draws to a plain-ASCII stand-in.
// It is applied to finished output (never to measured text), so one table
// covers the TUI, the report and the compact summary.
var asciiGlyphs = strings.NewReplacer(
	"≥", ">=", "≤", "<=", "…", "...", "→", "->", "←", "<-", "↑", "^", "↓", "v", "↕", "^",
	"◂", "<", "‹", "<", "›", ">", "⚠", "!", "ℹ", "i", "✓", "+", "◐", "~", "○", "o", "■", "#",
	"█", "#", "▓", "%", "▒", ":", "░", ".", "▏", "|", "▎", "|", "▍", "|", "▌", "|", "▋", "|", "▊", "|", "▉", "|",
	"▁", "_", "▂", ".", "▃", "-", "▄", "=", "▅", "+", "▆", "*", "▇", "#",
	"▲", "^", "▼", "v", "│", "|", "┃", "|", "─", "-", "━", "=", "╭", "+", "╮", "+", "╰", "+", "╯", "+",
	"●", "*", "↳", ">", "⏎", "Enter", "·", "-", "—", "-", "–", "-", "“", "\"", "”", "\"", "‘", "'", "’", "'",
	"⠋", "|", "⠙", "/", "⠹", "-", "⠸", "\\", "⠼", "|", "⠴", "/", "⠦", "-", "⠧", "\\", "⠇", "|", "⠏", "/",
)

// Fold returns s with ASCII stand-ins for every glyph when the theme is
// ASCII-only, and s unchanged otherwise.
func (t Theme) Fold(s string) string {
	if !t.ASCII {
		return s
	}
	return asciiGlyphs.Replace(s)
}

// ASCIIOnly replaces every glyph in s with its ASCII stand-in.
func ASCIIOnly(s string) string { return asciiGlyphs.Replace(s) }

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Strip removes ANSI colour codes.
func Strip(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Width is the display width of s, ignoring colour codes.
func Width(s string) int { return runewidth.StringWidth(Strip(s)) }

// Pad right-pads (or truncates with an ellipsis) to exactly w columns.
// Truncation can stop one column short of w (a wide character does not fit),
// so the padding is measured after it.
func (t Theme) Pad(s string, w int) string {
	if Width(s) > w {
		s = t.Trunc(s, w)
	}
	return s + strings.Repeat(" ", max(0, w-Width(s)))
}

// PadL left-pads to w columns.
func (t Theme) PadL(s string, w int) string {
	if Width(s) > w {
		s = t.Trunc(s, w)
	}
	return strings.Repeat(" ", max(0, w-Width(s))) + s
}

// Trunc shortens s to w display columns, keeping colour codes intact and
// closing any open colour before the ellipsis.
func (t Theme) Trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	ell := "…"
	if t.ASCII {
		ell = "~"
	}
	// A model id that ends in a snapshot date keeps the date: two snapshots
	// of one model differ only there, so cutting the end would make them look
	// the same. The ellipsis goes in the middle.
	if loc := snapshotDate.FindStringIndex(s); loc != nil && loc[0] > 0 && !strings.Contains(s, "\x1b") {
		tail := s[loc[0]:]
		if room := w - Width(tail) - Width(ell); room >= 1 {
			return prefixWidth(s[:loc[0]], room) + ell + tail
		}
	}
	budget := w - 1
	var b strings.Builder
	used, open := 0, false
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // copy an escape sequence without counting it
			j := i
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				seq := s[i : j+1]
				b.WriteString(seq)
				open = seq != "\x1b[0m"
				i = j + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if used+rw > budget {
			break
		}
		b.WriteString(s[i : i+size])
		used += rw
		i += size
	}
	if open {
		b.WriteString("\x1b[0m")
	}
	return b.String() + ell
}

// snapshotDate matches the date suffix of a model snapshot id (as pricing does).
var snapshotDate = regexp.MustCompile(`[-@]\d{8}$|-\d{4}-\d{2}-\d{2}$`)

// prefixWidth is the longest prefix of s that fits in w display columns.
func prefixWidth(s string, w int) string {
	used := 0
	for i, r := range s {
		used += runewidth.RuneWidth(r)
		if used > w {
			return s[:i]
		}
	}
	return s
}

// Tokens formats a token count compactly: 950, 12.3K, 4.56M, 1.20B.
func Tokens(n int64) string {
	f := float64(n)
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1e6:
		if s := fmt.Sprintf("%.1fK", f/1e3); s != "1000.0K" {
			return s
		}
		return "1.00M" // rounds up to 1000.0K: say it in the next unit
	case n < 1e9:
		if s := fmt.Sprintf("%.2fM", f/1e6); s != "1000.00M" {
			return s
		}
		return "1.00B"
	default:
		return fmt.Sprintf("%.2fB", f/1e9)
	}
}

// Bytes formats a byte count in decimal units (1 KB = 1000 B), as Tokens does for counts.
func Bytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%dB", n)
	}
	f, unit := float64(n)/1000, 0
	units := []string{"KB", "MB", "GB", "TB"}
	for f >= 999.95 && unit < len(units)-1 { // promote before rounding, as Tokens does
		f /= 1000
		unit++
	}
	return fmt.Sprintf("%.1f%s", f, units[unit])
}

// Num formats an integer with thousands separators.
func Num(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// Cost formats USD, with more precision for small amounts.
func Cost(c float64) string {
	switch {
	case c == 0:
		return "$0.00"
	case c < 0.01:
		return fmt.Sprintf("$%.4f", c)
	case c < 100:
		return fmt.Sprintf("$%.2f", c)
	default:
		return "$" + Num(int(math.Round(c)))
	}
}

// Pct formats a fraction as a percentage.
func Pct(f float64) string {
	if f >= 10 {
		return ">1000%"
	}
	if f < 0.001 && f > 0 {
		return "<0.1%"
	}
	if f >= 0.1 {
		return fmt.Sprintf("%.0f%%", f*100)
	}
	return fmt.Sprintf("%.1f%%", f*100)
}

// Bar draws a horizontal bar of w cells filled to frac.
func (t Theme) Bar(frac float64, w int) string {
	if w <= 0 {
		return ""
	}
	frac = math.Max(0, math.Min(1, frac))
	full, empty, parts := "█", "░", []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	if t.ASCII {
		full, empty, parts = "#", ".", nil
	}
	cells := frac * float64(w)
	n := int(cells)
	out := strings.Repeat(full, n)
	used := n
	if parts != nil && n < w {
		if p := int((cells - float64(n)) * 8); p > 0 {
			out += parts[p]
			used++
		}
	}
	return out + strings.Repeat(empty, w-used)
}

// Spark draws a sparkline of vals scaled to their maximum.
func (t Theme) Spark(vals []float64) string {
	levels := []rune("▁▂▃▄▅▆▇█")
	if t.ASCII {
		levels = []rune("._-=+*#@")
	}
	var mx float64
	for _, v := range vals {
		mx = math.Max(mx, v)
	}
	var b strings.Builder
	for _, v := range vals {
		switch {
		case mx == 0 || v == 0:
			b.WriteRune(' ')
		default:
			i := int(v / mx * float64(len(levels)-1))
			b.WriteRune(levels[i])
		}
	}
	return b.String()
}

// Arrow formats a change fraction with a direction glyph.
func (t Theme) Arrow(change float64) string {
	up, down, flat := "▲", "▼", "■"
	if t.ASCII {
		up, down, flat = "^", "v", "="
	}
	a := math.Abs(change)
	if a > 10 {
		a = 10 // ">1000%" is all anyone needs to know
	}
	switch {
	case a < 0.005:
		return flat + " flat"
	case change > 0:
		return t.Up(fmt.Sprintf("%s %s", up, Pct(a)))
	default:
		return t.Down(fmt.Sprintf("%s %s", down, Pct(a)))
	}
}

// DayBuckets sums consecutive days into at most n buckets of equal length, so
// a long window is drawn as weekly or longer bars instead of being cut. It
// returns the total of each bucket and the number of days in a bucket (1 when
// the days fit without grouping). The last bucket may hold fewer days.
func DayBuckets(days []*agg.Group, n int) ([]float64, int) {
	size := 1
	if n > 0 && len(days) > n {
		size = (len(days) + n - 1) / n
	}
	vals := make([]float64, (len(days)+size-1)/size)
	for i, d := range days {
		vals[i/size] += float64(d.Tokens())
	}
	return vals, size
}

// bucketNote names the bucket length for a chart line, or "" for single days.
func bucketNote(size int) string {
	switch size {
	case 1:
		return ""
	case 7:
		return "weekly buckets"
	default:
		return fmt.Sprintf("%d-day buckets", size)
	}
}

// peakBucket returns the index of the largest value (the first on a tie).
func peakBucket(vals []float64) int {
	best := 0
	for i, v := range vals {
		if v > vals[best] {
			best = i
		}
	}
	return best
}

// Bucket downsamples vals to at most n points by summing neighbours.
func Bucket(vals []float64, n int) []float64 {
	if n <= 0 || len(vals) <= n {
		return vals
	}
	out := make([]float64, n)
	for i, v := range vals {
		out[i*n/len(vals)] += v
	}
	return out
}

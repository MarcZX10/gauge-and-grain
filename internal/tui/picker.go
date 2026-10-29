package tui

import (
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/render"
)

// pickerView is the period picker: presets with their exact dates, and a
// free-text line that understands months, years, "14d" and date ranges.
func (m *model_) pickerView() []string {
	t, W := m.cfg.Theme, m.width
	now := time.Now().In(m.cfg.Loc)
	var lines []string
	for i, e := range pickerEntries() {
		from, to := e.p.window(now, m.cfg.Loc)
		dates := spanText(from, to, m.cfg.Loc)
		if dates == "" {
			if a, b := m.idx.Span(); !a.IsZero() {
				dates = spanText(a, b.AddDate(0, 0, 1), m.cfg.Loc)
			}
		}
		mark := "  "
		cur := e.p.preset == m.per.preset && e.p.shift == m.per.shift
		if cur {
			mark = t.FG(render.CGood, "✓ ")
		}
		l := mark + t.Pad(e.label, 16) + t.Dim(dates)
		if i == m.pickSel && m.pickIn == "" {
			l = t.Select(l, min(W, 80)-4)
		}
		lines = append(lines, l)
	}
	prompt := t.Bold("Or type") + " " + m.pickIn + t.FG(render.CAccent, "█")
	lines = append(lines, "", prompt)
	if m.pickErr != "" {
		lines = append(lines, t.Warn(m.pickErr))
	} else {
		lines = append(lines, t.Dim("2026-10   ·   2026-10-01..2026-10-15   ·   14d   ·   2026   ·   all"))
	}
	lines = append(lines, "", t.Dim("↑↓ choose · Enter apply · Esc cancel"), t.Dim("Afterwards  [ ] move the period earlier / later · . back to now"))
	w := min(W, 80)
	p := t.Panel("Choose a period", lines, w, render.CAccent)
	pad := strings.Repeat(" ", (W-w)/2)
	for i := range p {
		p[i] = pad + p[i]
	}
	return append([]string{""}, p...)
}

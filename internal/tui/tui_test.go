package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
	"github.com/MarcZX10/gauge-and-grain/internal/render"
	"github.com/MarcZX10/gauge-and-grain/internal/scan"
)

func fixtureModel(t *testing.T, th render.Theme) *model_ {
	home := t.TempDir()
	for from, to := range map[string]string{
		"claude/session-a.jsonl":         "claude/projects/p/a.jsonl",
		"claude/session-b-resumed.jsonl": "claude/projects/p/b.jsonl",
		"codex/records.jsonl":            "codex/sessions/2026/10/02/r.jsonl",
		"codex/legacy.jsonl":             "codex/sessions/2026/09/01/l.jsonl",
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", from))
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(home, to)), 0o755)
		os.WriteFile(filepath.Join(home, to), b, 0o644)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	pt, _ := pricing.Load("")
	provs := []providers.Provider{providers.Claude{}, providers.Codex{}}
	det := map[string]providers.Detection{}
	for _, p := range providers.All() {
		det[p.ID()] = p.Detect()
	}
	res := scan.Run(t.Context(), scan.Options{Providers: provs})
	m := &model_{cfg: Config{Providers: provs, Detect: det, Prices: pt, Theme: th, Loc: time.UTC, RangeName: "all time", All: true},
		rangeNm: "all time", res: res, events: res.Events, rangeIdx: len(ranges) - 1}
	m.recompute()
	return m
}

func resize(m *model_, w, h int) { m.Update(tea.WindowSizeMsg{Width: w, Height: h}) }
func press(m *model_, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.Update(msg)
	}
}

func TestEveryViewFitsEveryTerminalSize(t *testing.T) {
	for _, th := range []render.Theme{{Color: true}, {ASCII: true}} {
		m := fixtureModel(t, th)
		for _, sz := range [][2]int{{40, 8}, {44, 12}, {60, 20}, {80, 24}, {100, 30}, {120, 40}, {150, 46}, {220, 60}} {
			for tab := 0; tab < numTabs; tab++ {
				m.tab = tab
				resize(m, sz[0], sz[1])
				for step := 0; step < 3; step++ { // also scrolled/moved states
					out := m.View()
					lines := strings.Split(out, "\n")
					if len(lines) != sz[1] {
						t.Fatalf("%dx%d tab %d: %d lines, want %d", sz[0], sz[1], tab, len(lines), sz[1])
					}
					for i, l := range lines {
						if w := render.Width(l); w > sz[0] {
							t.Fatalf("%dx%d tab %d line %d is %d cols: %q", sz[0], sz[1], tab, i, w, render.Strip(l))
						}
					}
					press(m, "down", "down", "down")
				}
			}
		}
	}
}

func TestTooSmallTerminalAsksForMoreSpace(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 30, 6)
	if !strings.Contains(m.View(), "too small") {
		t.Error("expected a size message")
	}
}

func TestDrillDownBackAndFilters(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	all := m.sum.Total.Tokens()
	press(m, "4") // Models
	first := m.groups()[0]
	press(m, "enter")
	if m.tab != tabOverview || len(m.filter.Models) != 1 || m.sum.Total.Tokens() >= all || m.sum.Total.Tokens() != first.Tokens() {
		t.Fatalf("drill-down should narrow to the model: tab=%d tokens=%d want %d", m.tab, m.sum.Total.Tokens(), first.Tokens())
	}
	if !strings.Contains(m.View(), "model:") {
		t.Error("active filter must be visible as a chip")
	}
	press(m, "esc")
	if m.sum.Total.Tokens() != all || len(m.drill) != 0 {
		t.Error("esc must undo the drill-down")
	}
	press(m, "/", "g", "p", "t", "enter")
	if m.sum.Total.Tokens() >= all || m.filter.Text != "gpt" {
		t.Error("search should filter")
	}
	press(m, "c")
	if m.sum.Total.Tokens() != all {
		t.Error("c clears every filter")
	}
	press(m, "p")
	if m.provIdx != 1 || len(m.sum.ByProvider) != 1 {
		t.Error("p filters to one provider")
	}
}

func TestNotesTabIsVisibleAndFlagsWarnings(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true}) // fixtures contain an unpriced model
	resize(m, 120, 40)
	if !strings.Contains(m.tabBar(), "!") {
		t.Errorf("notes tab must show a warning badge: %q", m.tabBar())
	}
	if !strings.Contains(m.View(), "attention") {
		t.Error("overview must point at the notes")
	}
	press(m, "n")
	v := strings.Join(strings.Fields(m.View()), " ")
	for _, want := range []string{"Unpriced", "not your subscription bill", "Sources", "Prices"} {
		if !strings.Contains(v, want) {
			t.Errorf("notes tab lacks %q", want)
		}
	}
}

func TestMouseTabsRowsAndWheel(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	sp := m.tabSpans()
	m.Update(tea.MouseMsg{X: sp[3][0] + 1, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.tab != tabModels {
		t.Fatalf("click on tab 4 → tab %d", m.tab)
	}
	g := m.geometry()
	m.Update(tea.MouseMsg{X: 5, Y: g.tableTop + 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.cursor[tabModels] != 1 {
		t.Errorf("click on second row → cursor %d", m.cursor[tabModels])
	}
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if m.cursor[tabModels] <= 1 {
		t.Error("wheel should move the cursor")
	}
	rs := m.rangeSpans()
	m.Update(tea.MouseMsg{X: rs[0][0] + 1, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if ranges[m.rangeIdx] != 7 || m.filter.From.IsZero() {
		t.Error("click on the 7d chip should select the 7-day range")
	}
}

func TestCancelledScanShowsPartialNotice(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.cancelled = true
	resize(m, 100, 30)
	if !strings.Contains(m.View(), "PARTIAL") {
		t.Error("a cancelled scan must be marked partial in the status line")
	}
}

var _ = fmt.Sprint
var _ agg.Filter

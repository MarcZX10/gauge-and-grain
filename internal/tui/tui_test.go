package tui

import (
	"fmt"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
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
		rangeNm: "all time", res: res, events: res.Events, per: periodState{preset: presetIndex("all")}}
	m.idx = agg.NewIndex(res.Events, pt, time.UTC)
	m.applyPeriod()
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
	_, sp := m.tabLayout()
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
	_, chips := m.headerLayout()
	for _, c := range chips {
		if c.preset == presetIndex("7d") {
			m.Update(tea.MouseMsg{X: c.x0 + 1, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		}
	}
	if presets[m.per.preset].id != "7d" || m.filter.From.IsZero() {
		t.Error("click on the 7d chip should select the 7-day period")
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

func TestPeriodPickerAndShifting(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "t")
	if !m.picker || !strings.Contains(strings.Join(strings.Fields(m.View()), " "), "Choose a period") {
		t.Fatal("t must open the picker")
	}
	press(m, "down", "down", "down", "enter") // Last 30 days
	if m.picker || presets[m.per.preset].id != "30d" {
		t.Fatalf("picker selection: preset=%d picker=%v", m.per.preset, m.picker)
	}
	hdr := render.Strip(m.headerLine())
	if !strings.Contains(hdr, "30d") || !strings.Contains(hdr, "–") {
		t.Errorf("header must show the active chip and exact dates: %q", hdr)
	}
	from0 := m.filter.From
	press(m, "[")
	if m.per.shift != 1 || !m.filter.From.Before(from0) || !m.filter.To.Equal(from0) {
		t.Errorf("[ must move one period earlier: shift=%d", m.per.shift)
	}
	press(m, "]")
	if m.per.shift != 0 || !m.filter.From.Equal(from0) {
		t.Error("] must move back")
	}
	press(m, "]")
	if m.per.shift != 0 {
		t.Error("cannot move into the future")
	}
	press(m, "t", "2", "0", "2", "6", "-", "1", "0", "enter")
	if m.per.preset != customPreset || m.filter.From.Month() != time.October || m.filter.To.Month() != time.November {
		t.Errorf("typed month: %+v", m.per)
	}
	press(m, "t", "n", "o", "p", "e", "enter")
	if !m.picker || m.pickErr == "" {
		t.Error("a bad period must show an error and keep the picker open")
	}
	press(m, "esc")
	press(m, "d")
	if m.per.preset == customPreset {
		t.Error("d returns to the quick periods")
	}
}

func TestPickerAndHeaderFitEverySize(t *testing.T) {
	m := fixtureModel(t, render.Theme{Color: true})
	for _, sz := range [][2]int{{40, 8}, {60, 20}, {100, 30}, {200, 50}} {
		resize(m, sz[0], sz[1])
		m.picker = true
		for _, l := range strings.Split(m.View(), "\n") {
			if render.Width(l) > sz[0] {
				t.Fatalf("%v picker line too wide: %q", sz, render.Strip(l))
			}
		}
		m.picker = false
	}
}

func TestRepeatedFramesAreCachedAndCheap(t *testing.T) {
	m := fixtureModel(t, render.Theme{Color: true})
	resize(m, 150, 46)
	_ = m.View()
	v := m.cache[tabOverview].ver
	for i := 0; i < 200; i++ { // scrolling / mouse motion must not rebuild
		m.move(1)
		m.move(-1)
		_ = m.View()
	}
	if m.cache[tabOverview].ver != v || m.ver != v {
		t.Error("view cache was rebuilt without a data change")
	}
	press(m, "p")
	_ = m.View()
	if m.cache[tabOverview].ver == v {
		t.Error("a filter change must rebuild the view")
	}
}

// A keypress that changes the data (filter, period) must feel instant even
// with a very large history. The bound is generous (CI machines are slow);
// the old per-event implementation took ~3.5 s for 270k events.
func TestInteractionsAreFastOnLargeHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("large history")
	}
	pt, _ := pricing.Load("")
	var evs []model.Event
	models := []string{"claude-sonnet-5-5", "gpt-5.4", "claude-opus-5-5", "mystery"}
	for i := 0; i < 200000; i++ {
		evs = append(evs, model.Event{Provider: []string{"claude", "codex"}[i%2], Model: models[i%4],
			Project: fmt.Sprint("/w/p", i%30), Session: fmt.Sprint("s", i%400),
			Time:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i%(150*24*60)) * time.Minute),
			Input: 10, CacheRead: 100, Output: 5, Has: model.HasCacheRead})
	}
	res := &scan.Result{Events: evs, Files: map[string]int{"claude": 1, "codex": 1}}
	m := &model_{cfg: Config{Prices: pt, Theme: render.Theme{Color: true}, Loc: time.UTC, Detect: map[string]providers.Detection{}, Providers: []providers.Provider{providers.Claude{}, providers.Codex{}}},
		res: res, events: evs, per: periodState{preset: presetIndex("all")}}
	t0 := time.Now()
	m.idx = agg.NewIndex(evs, pt, time.UTC)
	m.applyPeriod()
	t.Logf("index + first summary: %v", time.Since(t0))
	resize(m, 150, 46)
	_ = m.View()
	for _, k := range []string{"p", "d", "d", "t", "esc", "[", "]", "4", "down", "enter", "esc", "s"} {
		start := time.Now()
		press(m, k)
		_ = m.View()
		if el := time.Since(start); el > 400*time.Millisecond {
			t.Errorf("key %q took %v on 200k events", k, el)
		}
	}
}

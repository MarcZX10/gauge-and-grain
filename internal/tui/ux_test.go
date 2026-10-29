package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MarcZX10/gauge-and-grain/internal/render"
)

// A provider filter given on the command line is shown, and "c" clears it.
func TestCommandLineFiltersAreShownAndCleared(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.filter.Providers = []string{"claude"}
	m.applyPeriod()
	if strings.Contains(strings.Join(m.filterChips(), "|"), "codex") || !strings.Contains(strings.Join(m.filterChips(), "|"), "provider: claude") {
		t.Fatalf("the command-line filter must be shown as a chip: %v", m.filterChips())
	}
	if _, ok := m.sum.Total.Providers["codex"]; ok {
		t.Fatal("the filter must be applied")
	}
	press(m, "c")
	if _, ok := m.sum.Total.Providers["codex"]; !ok {
		t.Error("c must clear the command-line provider filter too")
	}
	for _, c := range m.filterChips() {
		if strings.HasPrefix(c, "provider") {
			t.Errorf("no provider chip after c: %v", m.filterChips())
		}
	}
}

// "p" takes over from a command-line provider filter and can return to all.
func TestProviderCycleReplacesCommandLineFilter(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.filter.Providers = []string{"codex"}
	m.applyPeriod()
	press(m, "p") // first provider in the cycle
	if len(m.filter.Providers) != 0 || m.provIdx != 1 {
		t.Fatalf("p must drop the command-line filter: %v provIdx=%d", m.filter.Providers, m.provIdx)
	}
	for i := 0; i < len(m.provList()); i++ {
		press(m, "p")
	}
	if m.provIdx != 0 || len(m.sum.Total.Providers) < 2 {
		t.Errorf("a full cycle must return to all providers: provIdx=%d providers=%v", m.provIdx, m.sum.Total.Providers)
	}
}

// The picker is modal: clicks outside it do nothing, clicks on its rows choose.
func TestPickerIsModalAndClickable(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "t")
	before := m.per.preset
	m.Update(tea.MouseMsg{X: 3, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !m.picker || m.per.preset != before {
		t.Fatal("a click on the header must not change the period while the picker is open")
	}
	// entries start two rows below the body's top (blank line, panel border)
	idx := 2 // "Last 7 days"
	m.Update(tea.MouseMsg{X: 10, Y: bodyTop + 2 + idx, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.picker || m.per.preset != presetIndex("7d") {
		t.Errorf("clicking an entry must choose it: picker=%v preset=%d", m.picker, m.per.preset)
	}
}

// q quits during the first scan, and Esc only cancels it.
func TestQuitDuringFirstScan(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.loading = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil || cmd() != tea.Quit() {
		t.Error("q during the scan must quit")
	}
	m.loading = true
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil && cmd() == tea.Quit() {
		t.Error("Esc during the scan must cancel, not quit")
	}
}

// q quits from the help screen too, the way the help says it does.
func TestQuitFromHelp(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	press(m, "?")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil || cmd() != tea.Quit() {
		t.Error("q in the help screen must quit")
	}
}

// Sessions have nothing finer to drill into: Enter says so instead of doing nothing.
func TestSessionsSayWhyEnterDoesNothing(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "6", "enter")
	if !strings.Contains(m.status(), "finest level") {
		t.Errorf("status after Enter on Sessions: %q", m.status())
	}
}

// "e" writes the current view as CSV in the working directory.
func TestExportWritesTheCurrentView(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	t.Chdir(t.TempDir())
	press(m, "3", "e") // Providers
	files, _ := filepath.Glob("gg-provider-*.csv")
	if len(files) != 1 || !strings.HasPrefix(m.note, "exported provider") {
		t.Fatalf("expected one provider CSV, note=%q files=%v", m.note, files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.HasPrefix(string(b), "key,label,") || !strings.Contains(string(b), "\nclaude,claude,") {
		t.Errorf("CSV content: %s", b)
	}
}

// Every view, in ASCII mode, draws only ASCII.
func TestAsciiModeDrawsOnlyAscii(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	for _, k := range []string{"1", "2", "3", "4", "5", "6", "7", "t", "esc", "?", "esc"} {
		press(m, k)
		for _, r := range render.Strip(m.View()) {
			if r >= utf8.RuneSelf {
				t.Fatalf("view after %q has a non-ASCII rune %q", k, r)
			}
		}
	}
}

// Narrow terminals keep the filter count and the Notes warning visible.
func TestNarrowTerminalKeepsFiltersAndWarnings(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.filter.Providers = []string{"claude"}
	m.filter.Models = []string{"sonnet"}
	m.filter.Projects = []string{"p"}
	m.applyPeriod()
	resize(m, 60, 30)
	if !strings.Contains(m.status(), "model: sonnet") || !strings.Contains(m.status(), "project: p") {
		t.Errorf("narrow status must name the filters: %q", m.status())
	}
	m.warn = 2
	m.tab = tabDaily
	if _, sp := m.tabLayout(); len(sp) == 0 {
		t.Fatal("tab layout")
	}
	if l, _ := m.tabLayout(); !strings.Contains(render.Strip(l[0]), "!2") && !strings.Contains(render.Strip(l[0]), "⚠2") {
		t.Errorf("the Notes warning must still show with one tab: %q", render.Strip(l[0]))
	}
}

// A background rescan keeps the view and says so; the view is never blanked.
func TestBackgroundRefreshKeepsTheView(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	_, cmd := m.Update(refreshMsg{})
	if cmd == nil || !m.refreshing || m.loading {
		t.Fatalf("a refresh runs in the background: refreshing=%v loading=%v", m.refreshing, m.loading)
	}
	if !strings.Contains(m.status(), "updating") {
		t.Errorf("status while refreshing: %q", m.status())
	}
	m.Update(doneMsg(m.res))
	if m.refreshing || m.updated.IsZero() || m.loading {
		t.Error("finishing a refresh must clear the flag and record the time")
	}
	if !strings.Contains(m.status(), "updated "+m.updated.Format("15:04")) {
		t.Errorf("status after refresh: %q", m.status())
	}
}

// The preferences survive a restart, but a period given on the command line wins.
func TestPreferencesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.json")
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "s", "3") // by cost, Providers
	m.setPeriod(periodState{preset: presetIndex("7d")})
	m.compact = true
	m.cfg.Prefs = path
	m.savePrefs()

	n := fixtureModel(t, render.Theme{ASCII: true})
	n.cfg.Prefs = path
	n.loadPrefs()
	if n.sort != byCost || n.tab != tabProviders || n.per.preset != presetIndex("7d") || !n.compact {
		t.Errorf("preferences not restored: sort=%v tab=%d preset=%d compact=%v", n.sort, n.tab, n.per.preset, n.compact)
	}

	x := fixtureModel(t, render.Theme{ASCII: true})
	x.cfg.Prefs, x.cfg.Explicit = path, true
	x.per = periodState{preset: presetIndex("all")}
	x.loadPrefs()
	if x.per.preset != presetIndex("all") {
		t.Error("a period given on the command line must win over the saved one")
	}
}

// Pointing at a bar previews a day but leaves the cursor and Enter alone.
func TestGlyphFoldIsCompleteForAscii(t *testing.T) {
	out := (render.Theme{ASCII: true}).Fold("≥ $1 · ▲ 3% ▼ 2% ⚠ █░ ◂1 ✓ ✓ → ← ↑↓ ↕ ◐ ○ ■ │─┃━╭╮╰╯ “x” — – …")
	for _, r := range out {
		if r >= utf8.RuneSelf {
			t.Fatalf("non-ASCII left after fold: %q in %q", r, out)
		}
	}
	if (render.Theme{}).Fold("█") != "█" {
		t.Error("the fold must not touch colour output")
	}
}

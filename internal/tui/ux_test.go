package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
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

// The calendar shades the busiest day darkest and leaves days outside the data blank.
func TestActivityCalendar(t *testing.T) {
	var days []*agg.Group
	for i, tok := range []int64{0, 10, 100, 0, 50} { // 2026-10-05 (Mon) .. 2026-10-09 (Fri)
		g := &agg.Group{Key: "2026-10-0" + string(rune('5'+i)), Label: "x"}
		g.Input = tok
		days = append(days, g)
	}
	lines := heatmapLines(render.Theme{ASCII: true}, days, 40)
	if len(lines) != 7 {
		t.Fatalf("one row per weekday, got %d", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "█") || !strings.Contains(joined, "·") {
		t.Errorf("the busiest day is a full block and an idle day a dot: %q", joined)
	}
	if heatGlyph(0, 100) != "·" || heatGlyph(100, 100) != "█" || heatGlyph(1, 100) != "░" {
		t.Error("shades")
	}
}

// The calendar appears on the Daily view when there is room, and not in a short terminal.
func TestDailyCalendarOnlyWhenThereIsRoom(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "2")
	if m.geometry().heatH == 0 || !strings.Contains(m.View(), "Activity") {
		t.Error("a tall terminal shows the activity calendar")
	}
	resize(m, 120, 24)
	if m.geometry().heatH != 0 {
		t.Error("a short terminal keeps the chart and table")
	}
}

// The Tools view lists tools and skills from the transcripts, and says so when there are none.
func TestToolsView(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "7")
	if !strings.Contains(m.View(), "No tool calls") {
		t.Error("the fixture has no tool calls: the view must say so")
	}
	for i := range m.events {
		m.events[i].Tools = "Bash,Read,skill:review"
	}
	m.idx = agg.NewIndex(m.events, m.cfg.Prices, m.cfg.Loc)
	m.applyPeriod()
	v := render.Strip(m.View())
	if !strings.Contains(v, "Bash") || !strings.Contains(v, "skill:review") || !strings.Contains(v, "Skills") {
		t.Errorf("tools and skills must be listed:\n%s", v)
	}
	if !strings.Contains(v, "Notes") {
		t.Error("the tab bar keeps Notes")
	}
}

// After an upgrade the first result comes from the previous cache: the view
// shows it at once, says so, and a full scan follows, once per session.
func TestStaleResultShowsAtOnceAndRefreshesOnce(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	stale := *m.res
	stale.Stale = 2
	m.loading = true
	_, cmd := m.Update(doneMsg(&stale))
	if m.loading || cmd == nil || !m.refreshing {
		t.Fatalf("the stale result must show at once and start the full scan: loading=%v refreshing=%v cmd=%v", m.loading, m.refreshing, cmd != nil)
	}
	if !strings.Contains(m.status(), "2 files updating from the previous version") {
		t.Errorf("status while the old cache shows: %q", m.status())
	}
	fresh := *m.res
	fresh.Stale = 0
	_, cmd = m.Update(doneMsg(&fresh))
	if m.refreshing || cmd != nil || strings.Contains(m.status(), "previous version") {
		t.Errorf("a full result ends the update: refreshing=%v cmd=%v status=%q", m.refreshing, cmd != nil, m.status())
	}
	again := stale // the cache could not be rewritten: no second full scan
	_, cmd = m.Update(doneMsg(&again))
	if cmd != nil {
		t.Error("the full scan runs once per session, not in a loop")
	}
}

// With no usage in the filter, the Daily view draws its calendar panel
// without indexing the empty day list.
func TestDailyWithNoDataDoesNotPanic(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "2")
	m.filter.Text = "no-such-model-anywhere"
	m.recompute()
	if len(m.sum.ByDay) != 0 || m.geometry().heatH == 0 {
		t.Fatalf("the test needs an empty Daily view with the calendar: days=%d heatH=%d", len(m.sum.ByDay), m.geometry().heatH)
	}
	if v := m.View(); !strings.Contains(v, "no data") {
		t.Errorf("an empty Daily view must say so:\n%s", render.Strip(v))
	}
}

// A background refresh keeps the place in the table: the cursor stays on its row.
func TestBackgroundRefreshKeepsTheCursor(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "4", "down")
	if m.cursor[tabModels] != 1 {
		t.Fatalf("the test needs the cursor on row 1, got %d", m.cursor[tabModels])
	}
	m.Update(doneMsg(m.res))
	if m.cursor[tabModels] != 1 {
		t.Errorf("a refresh must keep the cursor: got %d", m.cursor[tabModels])
	}
}

// A day drill is kept by a refresh, and a period change is what ends it.
func TestDayDrillSurvivesARefresh(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "2", "enter") // Daily, drill into the latest day
	day := m.filter.From
	if day.IsZero() || len(m.drill) != 1 {
		t.Fatalf("the test needs a day drill: drill=%v", m.drill)
	}
	m.Update(doneMsg(m.res))
	if !m.filter.From.Equal(day) || !m.filter.To.Equal(day.AddDate(0, 0, 1)) || len(m.drill) != 1 {
		t.Errorf("a refresh must keep the day drill: from=%v drill=%v", m.filter.From, m.drill)
	}
	press(m, "d") // a quick period replaces the day
	if len(m.drill) != 0 || m.filter.From.Equal(day) {
		t.Errorf("a period change must end the day drill: drill=%v from=%v", m.drill, m.filter.From)
	}
}

// Esc during a background refresh cancels it instead of going back, and the
// cancelled scan starts no follow-up scan and does not replace the view.
func TestEscCancelsARefreshWithoutANewScan(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "4")
	_, cmd := m.Update(refreshMsg{})
	if cmd == nil || !m.refreshing || m.loading {
		t.Fatalf("the test needs a background refresh: refreshing=%v loading=%v", m.refreshing, m.loading)
	}
	cancelled := false
	real := m.cancel
	m.cancel = func() { cancelled = true; real() }
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled || cmd != nil || m.tab != tabModels {
		t.Errorf("Esc during a refresh must cancel it and not go back: cancelled=%v tab=%d", cancelled, m.tab)
	}
	c := *m.res
	c.Cancelled = true
	_, cmd = m.Update(doneMsg(&c))
	if cmd != nil {
		t.Error("a cancelled refresh must not start another scan")
	}
	if m.refreshing || strings.Contains(m.status(), "PARTIAL") {
		t.Errorf("a cancelled refresh ends quietly: refreshing=%v status=%q", m.refreshing, m.status())
	}
}

// r with data on screen rescans in the background: the view stays, the keys
// keep working, and Esc stops the rescan with the last complete view kept.
func TestRescanKeepsTheViewAndEscKeepsIt(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "4", "r")
	if m.loading || !m.refreshing {
		t.Fatalf("r with data loaded must refresh in place: loading=%v refreshing=%v", m.loading, m.refreshing)
	}
	if v := render.Strip(m.View()); strings.Contains(v, "Reading local usage files") || !strings.Contains(v, "Models") {
		t.Errorf("the view must stay on the data during a rescan:\n%s", v)
	}
	press(m, "down")
	if m.cursor[tabModels] != 1 {
		t.Errorf("keys must work during a rescan: cursor=%d", m.cursor[tabModels])
	}
	press(m, "esc")
	c := *m.res
	c.Cancelled = true
	m.Update(doneMsg(&c))
	if m.loading || m.refreshing || m.tab != tabModels {
		t.Errorf("Esc must end the rescan and keep the view: loading=%v refreshing=%v tab=%d", m.loading, m.refreshing, m.tab)
	}
	if v := render.Strip(m.View()); strings.Contains(v, "Reading local usage files") || !strings.Contains(v, "Models") {
		t.Errorf("the last complete view must stay after Esc:\n%s", v)
	}
}

// gatedClaude holds its scan at the start until the gate opens, so a test
// decides when the scan produces its messages.
type gatedClaude struct {
	providers.Claude
	gate chan struct{}
}

func (g gatedClaude) Files() []string {
	<-g.gate
	return g.Claude.Files()
}

// A scan that was replaced while its listener had stopped reading must end,
// even when its channel is full: its final message has no reader.
func TestReplacedScanDoesNotBlockForever(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	gate := make(chan struct{})
	m.cfg.Providers = []providers.Provider{gatedClaude{providers.Claude{}, gate}}
	before := runtime.NumGoroutine()
	m.startScan(false)
	for i := 0; i < cap(m.ch); i++ { // the listener is gone: the buffer fills up
		m.ch <- progressMsg{}
	}
	m.startScan(false) // replaces the first scan
	close(gate)
	for deadline := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > before; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("replaced scan goroutines did not end: %d before, %d after", before, runtime.NumGoroutine())
		}
	}
}

// A cancelled first scan with stale files starts no full rescan.
func TestCancelledFirstScanStartsNoFollowUp(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	stale := *m.res
	stale.Stale, stale.Cancelled = 2, true
	m.loading = true
	_, cmd := m.Update(doneMsg(&stale))
	if cmd != nil || m.refreshing {
		t.Errorf("a cancelled scan must not start the follow-up: cmd=%v refreshing=%v", cmd != nil, m.refreshing)
	}
}

// Back from a provider drill returns to the provider the view showed before it,
// not to "all providers".
func TestBackRestoresTheProviderFilter(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "p", "p", "3") // Claude, then Codex, then Providers: only Codex is listed
	if m.provIdx != 2 || len(m.groups()) != 1 {
		t.Fatalf("the test needs the Codex filter: provIdx=%d rows=%d", m.provIdx, len(m.groups()))
	}
	press(m, "enter", "esc")
	if m.provIdx != 2 || len(m.drill) != 0 {
		t.Errorf("Esc from a provider drill must restore the Codex filter: provIdx=%d drill=%v", m.provIdx, m.drill)
	}
	press(m, "c", "3", "enter", "esc") // from all providers, the same drill returns to all
	if m.provIdx != 0 {
		t.Errorf("Esc must return to all providers when the drill started there: provIdx=%d", m.provIdx)
	}
}

// Two scans in a row: a result from the replaced one must not end the newer one.
func TestOlderScanResultsAreIgnored(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	m.loading = true
	m.startScan(false)
	older := m.seq
	m.startScan(false)
	if m.seq != older+1 {
		t.Fatalf("each scan gets its own number: %d then %d", older, m.seq)
	}
	m.Update(scanMsg{older, doneMsg(m.res)})
	if !m.loading {
		t.Error("a result from a replaced scan must not end the newer scan")
	}
	m.Update(scanMsg{m.seq, doneMsg(m.res)})
	if m.loading {
		t.Error("the newest scan's result must end the load")
	}
}

// The cost change against the previous period is shown only when both periods
// are fully priced; an unpriced previous period is "n/a", not a change.
func TestCostChangeNeedsACompletePreviousPeriod(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	m.filter.Providers = []string{"codex"} // every codex model has a price: the total is complete
	m.applyPeriod()
	if !m.sum.Total.Complete() {
		t.Fatal("the test needs a complete total")
	}
	m.sum.Prev = &agg.Group{Input: 1000, Cost: 1, PricedTok: 500, UnpricedTok: 500}
	m.ver++
	if out := render.Strip(strings.Join(m.overviewLines(), "\n")); !strings.Contains(out, "n/a vs") {
		t.Errorf("an unpriced previous period must not give a change:\n%s", out)
	}
	m.sum.Prev = &agg.Group{Input: 1000, Cost: 1, PricedTok: 1000}
	m.ver++
	if out := render.Strip(strings.Join(m.overviewLines(), "\n")); !strings.Contains(out, "estimate") {
		t.Errorf("two priced periods must show the change:\n%s", out)
	}
}

// The space key types exactly one space, in the search box and in the period picker.
func TestSpaceTypesOneSpace(t *testing.T) {
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	press(m, "/")
	m.Update(space)
	press(m, "x")
	if m.input != " x" {
		t.Errorf("search must take the space: %q", m.input)
	}
	press(m, "esc", "t", "7")
	m.Update(space)
	press(m, "d")
	if m.pickIn != "7 d" {
		t.Errorf("the picker must insert one space: %q", m.pickIn)
	}
}

// "e" exports only the tables. On the text tabs it writes nothing and says why.
func TestExportNeedsATable(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	resize(m, 120, 40)
	t.Chdir(t.TempDir())
	press(m, "o", "e")
	press(m, "n", "e")
	files, _ := filepath.Glob("gg-*.csv")
	if len(files) != 0 || !strings.Contains(m.note, "nothing to export") {
		t.Errorf("the text tabs must write nothing: note=%q files=%v", m.note, files)
	}
	if !strings.Contains(render.Strip(strings.Join(m.helpView(), "\n")), "Daily to Sessions") {
		t.Error("the help must say which tabs export")
	}
}

// A planted temp file next to the preferences is not written through, and
// saves do not collide with each other.
func TestPreferencesSaveIgnoresTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tui.json")
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	os.Symlink(victim, path+".tmp")
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.cfg.Prefs = path
	m.savePrefs()
	m.savePrefs()
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("a save must not write through a planted symlink: %q", b)
	}
	b, _ := os.ReadFile(path)
	var p prefs
	if json.Unmarshal(b, &p) != nil {
		t.Errorf("preferences file after two saves: %q", b)
	}
}

// A loading box in a terminal of the minimum height still fits on screen.
func TestLoadingBoxFitsAShortTerminal(t *testing.T) {
	m := fixtureModel(t, render.Theme{ASCII: true})
	m.loading = true
	for _, h := range []int{minH, 9, 12, 40} {
		resize(m, 80, h)
		if rows := strings.Count(m.View(), "\n") + 1; rows > h {
			t.Errorf("loading view at height %d has %d rows", h, rows)
		}
	}
}

// In ASCII mode no line passes the terminal width, panels keep their width
// and their right border on every tab, at every terminal width: a glyph that
// grows when it is folded must not push a line past its panel.
func TestAsciiPanelsKeepTheirBorders(t *testing.T) {
	m := fixtureModel(t, render.Theme{})
	for w := 40; w <= 120; w++ {
		resize(m, w, 30)
		overlays := []string{""}
		if w%10 == 0 { // the help and the picker are the wider overlays: a few widths are enough
			overlays = append(overlays, "help", "picker")
		}
		for _, overlay := range overlays {
			for tab := 0; tab < numTabs; tab++ {
				m.tab, m.help, m.picker = tab, overlay == "help", overlay == "picker"
				m.cfg.Theme = render.Theme{}
				m.ver++
				uni := strings.Split(render.Strip(m.View()), "\n")
				m.cfg.Theme = render.Theme{ASCII: true}
				m.ver++
				asc := strings.Split(render.Strip(m.View()), "\n")
				if len(uni) != len(asc) {
					t.Fatalf("width %d tab %d %s: %d lines in ASCII, %d in Unicode", w, tab, overlay, len(asc), len(uni))
				}
				for i := range uni {
					ur, _ := utf8.DecodeLastRuneInString(uni[i])
					ar, _ := utf8.DecodeLastRuneInString(asc[i])
					uw, aw := render.Width(uni[i]), render.Width(asc[i])
					full := uw == w // a panel row or a full-width line
					if aw > w || full && aw != w || strings.ContainsRune("│┃╮╯", ur) && !strings.ContainsRune("|+#", ar) {
						t.Fatalf("width %d tab %d %s line %d:\nASCII   %q (%d)\nUnicode %q (%d)", w, tab, overlay, i, asc[i], aw, uni[i], uw)
					}
				}
			}
		}
	}
}

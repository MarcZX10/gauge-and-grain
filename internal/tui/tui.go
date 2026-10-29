// Package tui is the interactive view. It reuses the report's renderers and
// notes so the TUI and `gg report` can never disagree.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
	"github.com/MarcZX10/gauge-and-grain/internal/render"
	"github.com/MarcZX10/gauge-and-grain/internal/scan"
)

// Config is everything the TUI needs from the command line.
type Config struct {
	Providers []providers.Provider
	Detect    map[string]providers.Detection
	Prices    *pricing.Table
	CacheDir  string
	Filter    agg.Filter
	RangeName string
	Theme     render.Theme
	Loc       *time.Location
	DaysFlag  int
	All       bool
	Bounded   bool // range came from --days/--all, so presets may replace it
}

const (
	minW, minH = 40, 8
	chrome     = 5 // header, tabs, rule, two footer lines
	bodyTop    = 3 // first body row on screen
)

const (
	tabOverview = iota
	tabDaily
	tabProviders
	tabModels
	tabProjects
	tabSessions
	tabNotes
	numTabs
)

var tabNames = [numTabs]string{"Overview", "Daily", "Providers", "Models", "Projects", "Sessions", "Notes"}
var tabShort = [numTabs]string{"Ovw", "Day", "Prv", "Mdl", "Prj", "Ses", "Nts"}
var tabLabel = [numTabs]string{"", "Day", "Provider", "Model", "Project", "Session", ""}

type sortBy int

const (
	byTokens sortBy = iota
	byCost
)

type model_ struct {
	cfg    Config
	width  int
	height int

	loading   bool
	started   time.Time
	frame     int
	cancel    context.CancelFunc
	ch        chan tea.Msg
	prog      scan.Progress
	res       *scan.Result
	events    []model.Event
	cancelled bool
	scanTook  time.Duration

	tab     int
	cursor  [numTabs]int
	offset  [numTabs]int
	filter  agg.Filter
	per     periodState
	picker  bool
	pickSel int
	pickIn  string
	pickErr string
	provIdx int // 0 = all
	drill   []string
	idx     *agg.Index
	sum     *agg.Summary
	cx      *render.Ctx // rebuilt by recompute; shared by every view
	warn    int         // notes that need attention
	ver     int         // bumped on every recompute; keys the render caches
	cache   [numTabs]struct {
		ver, w int
		lines  []string
	}
	sort    sortBy
	search  bool
	input   string
	help    bool
	rangeNm string
}

type progressMsg scan.Progress
type doneMsg *scan.Result
type tickMsg time.Time
type startedMsg struct {
	ch     chan tea.Msg
	cancel context.CancelFunc
}

// Run starts the TUI and blocks until the user quits.
func Run(cfg Config) error {
	m := &model_{cfg: cfg, filter: cfg.Filter, rangeNm: cfg.RangeName, loading: true, started: time.Now()}
	m.per = initialPeriod(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	if m.cancel != nil {
		m.cancel()
	}
	return err
}

func (m *model_) Init() tea.Cmd { return tea.Batch(m.startScan(), tick()) }

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model_) startScan() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan tea.Msg, 64)
		go func() {
			res := scan.Run(ctx, scan.Options{Providers: m.cfg.Providers, CacheDir: m.cfg.CacheDir, Progress: func(p scan.Progress) {
				select { // never block the scan on a slow UI
				case ch <- progressMsg(p):
				default:
				}
			}})
			ch <- doneMsg(res)
		}()
		return startedMsg{ch, cancel}
	}
}

func listen(ch chan tea.Msg) tea.Cmd { return func() tea.Msg { return <-ch } }

func (m *model_) provList() []string {
	var out []string
	for _, p := range m.cfg.Providers {
		out = append(out, p.ID())
	}
	return out
}

func (m *model_) recompute() {
	if m.res == nil {
		return
	}
	f := m.filter
	if m.provIdx > 0 {
		f.Providers = []string{m.provList()[m.provIdx-1]}
	}
	m.sum = m.idx.Summarize(f)
	if m.sort == byCost {
		for _, gs := range [][]*agg.Group{m.sum.ByProvider, m.sum.ByModel, m.sum.ByProject, m.sum.BySession} {
			sort.SliceStable(gs, func(i, j int) bool { return gs[i].Cost > gs[j].Cost })
		}
	}
	for i := range m.cursor {
		m.cursor[i], m.offset[i] = 0, 0
	}
	m.cx = &render.Ctx{Sum: m.sum, Detect: m.cfg.Detect, Files: m.res.Files, Stats: m.res.Stats,
		Cached: m.res.Cached, Parsed: m.res.Parsed, Duplicate: m.res.Duplicate, Errors: len(m.res.Errors),
		Cancelled: m.cancelled, Prices: m.cfg.Prices, RangeName: m.rangeNm, Filters: m.filterText()}
	m.warn, _ = render.NoteCounts(m.cx)
	m.ver++
}

func (m *model_) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case startedMsg:
		m.ch, m.cancel = msg.ch, msg.cancel
		return m, listen(m.ch)
	case progressMsg:
		m.prog = scan.Progress(msg)
		return m, listen(m.ch)
	case tickMsg:
		if m.loading {
			m.frame++
			return m, tick()
		}
	case doneMsg:
		m.res = (*scan.Result)(msg)
		m.events = m.res.Events
		m.cancelled = m.res.Cancelled
		m.loading = false
		m.scanTook = time.Since(m.started)
		m.idx = agg.NewIndex(m.events, m.cfg.Prices, m.cfg.Loc)
		m.applyPeriod()
	case tea.MouseMsg:
		return m.mouse(msg)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// ---- keys ----------------------------------------------------------------

func (m *model_) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.loading {
		if s == "esc" || s == "q" {
			// Cancel cleanly: the scan stops, finished files stay cached
			// and whatever was read so far is shown as partial.
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	}
	if m.picker {
		return m.pickerKey(k)
	}
	if m.search {
		switch s {
		case "enter":
			m.search = false
			m.filter.Text = m.input
			m.recompute()
		case "esc":
			m.search, m.input = false, m.filter.Text
		case "backspace":
			if r := []rune(m.input); len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		default:
			if k.Type == tea.KeyRunes {
				m.input += string(k.Runes)
			}
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % numTabs
	case "shift+tab", "left", "h":
		m.tab = (m.tab + numTabs - 1) % numTabs
	case "1", "2", "3", "4", "5", "6", "7":
		m.tab = int(s[0] - '1')
	case "o":
		m.tab = tabOverview
	case "n":
		m.tab = tabNotes
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-m.pageSize())
	case "pgdown":
		m.move(m.pageSize())
	case "home", "g":
		m.cursor[m.tab], m.offset[m.tab] = 0, 0
	case "end", "G":
		m.move(1 << 20)
	case "enter":
		m.drillDown()
	case "esc", "backspace":
		m.back()
	case "d":
		m.cycleChip(1)
	case "D":
		m.cycleChip(-1)
	case "t":
		m.picker, m.pickIn, m.pickErr, m.pickSel = true, "", "", 0
	case "[":
		m.setPeriod(m.per.shifted(-1))
	case "]":
		m.setPeriod(m.per.shifted(1))
	case ".":
		m.setPeriod(periodState{preset: m.per.preset, from: m.per.from, to: m.per.to, label: m.per.label})
	case "p":
		m.provIdx = (m.provIdx + 1) % (len(m.provList()) + 1)
		m.recompute()
	case "s":
		m.sort = 1 - m.sort
		m.recompute()
	case "/":
		m.search, m.input = true, m.filter.Text
	case "c":
		m.filter.Text, m.filter.Models, m.filter.Projects, m.drill = "", nil, nil, nil
		m.provIdx = 0
		m.applyPeriod()
	case "r", "R":
		m.loading, m.cancelled, m.prog, m.started = true, false, scan.Progress{}, time.Now()
		return m, tea.Batch(m.startScan(), tick())
	}
	return m, nil
}

// ---- mouse ---------------------------------------------------------------

func (m *model_) mouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.loading || m.help || m.search || m.sum == nil {
		return m, nil
	}
	switch {
	case e.Button == tea.MouseButtonWheelUp:
		m.move(-3)
	case e.Button == tea.MouseButtonWheelDown:
		m.move(3)
	case e.Button == tea.MouseButtonLeft && e.Action == tea.MouseActionPress:
		switch {
		case e.Y == 0:
			_, sp := m.headerLayout()
			for _, c := range sp {
				if e.X >= c.x0 && e.X < c.x1 {
					if c.preset == pickerChip {
						m.picker, m.pickIn, m.pickErr, m.pickSel = true, "", "", 0
					} else {
						m.setPeriod(periodState{preset: c.preset})
					}
				}
			}
		case e.Y == 1:
			_, sp := m.tabLayout()
			for i, c := range sp {
				if e.X >= c[0] && e.X < c[1] {
					m.tab = i
				}
			}
		default:
			g := m.geometry()
			if m.isTable() && e.X < g.tableW {
				row := e.Y - (g.tableTop + 2) // border, then header
				if row >= 0 && row < g.vis {
					idx := m.offset[m.tab] + row
					if idx < len(m.groups()) {
						if idx == m.cursor[m.tab] {
							m.drillDown() // second click on the same row drills in
						} else {
							m.cursor[m.tab] = idx
						}
					}
				}
			}
		}
	}
	return m, nil
}

// ---- state changes -------------------------------------------------------

func (m *model_) isTable() bool { return m.tab >= tabDaily && m.tab <= tabSessions }

func (m *model_) groups() []*agg.Group {
	if m.sum == nil {
		return nil
	}
	switch m.tab {
	case tabDaily:
		out := make([]*agg.Group, len(m.sum.ByDay))
		for i, g := range m.sum.ByDay {
			out[len(out)-1-i] = g
		}
		return out
	case tabProviders:
		return m.sum.ByProvider
	case tabModels:
		return m.sum.ByModel
	case tabProjects:
		return m.sum.ByProject
	case tabSessions:
		return m.sum.BySession
	}
	return nil
}

func (m *model_) bodyH() int { return max(1, m.height-chrome) }

func (m *model_) pageSize() int {
	if m.isTable() {
		return max(1, m.geometry().vis-1)
	}
	return max(1, m.bodyH()-1)
}

func (m *model_) move(d int) {
	if !m.isTable() { // overview and notes scroll as text
		n := len(m.scrollLines())
		m.offset[m.tab] = max(0, min(m.offset[m.tab]+d, max(0, n-m.bodyH())))
		return
	}
	n := len(m.groups())
	if n == 0 {
		return
	}
	c := max(0, min(m.cursor[m.tab]+d, n-1))
	m.cursor[m.tab] = c
	vis := max(1, m.geometry().vis)
	if c < m.offset[m.tab] {
		m.offset[m.tab] = c
	}
	if c >= m.offset[m.tab]+vis {
		m.offset[m.tab] = c - vis + 1
	}
}

func (m *model_) back() {
	if len(m.drill) > 0 {
		last := m.drill[len(m.drill)-1]
		m.drill = m.drill[:len(m.drill)-1]
		kind, _, _ := strings.Cut(last, ":")
		switch kind {
		case "provider":
			m.provIdx = 0
		case "model":
			m.filter.Models = nil
		case "project":
			m.filter.Projects = nil
		case "day":
			m.applyPeriod()
			return
		}
		m.recompute()
		return
	}
	if m.filter.Text != "" {
		m.filter.Text = ""
		m.recompute()
		return
	}
	m.tab = tabOverview
}

// chipPresets are the presets shown as header chips, in order.
func chipPresets() []int {
	var out []int
	for i, p := range presets {
		if p.chip != "" {
			out = append(out, i)
		}
	}
	return out
}

// cycleChip moves to the next/previous quick period (Today, 7d, 30d, 90d, All).
func (m *model_) cycleChip(d int) {
	chips := chipPresets()
	cur := -1
	for i, c := range chips {
		if c == m.per.preset {
			cur = i
		}
	}
	if cur < 0 { // on a calendar/custom period: start from the 30-day chip
		cur = 2
	}
	m.setPeriod(periodState{preset: chips[(cur+d+len(chips))%len(chips)]})
}

func (m *model_) setPeriod(p periodState) {
	m.per = p
	m.applyPeriod()
}

// applyPeriod turns the selected period into a filter and recomputes.
func (m *model_) applyPeriod() {
	now := time.Now().In(m.cfg.Loc)
	m.filter.From, m.filter.To = m.per.window(now, m.cfg.Loc)
	m.filter.PrevFrom, m.filter.PrevTo = m.per.previous(now, m.cfg.Loc)
	m.rangeNm = m.per.name()
	if d := spanText(m.filter.From, m.filter.To, m.cfg.Loc); d != "" {
		m.rangeNm += " · " + d
	} else if m.idx != nil {
		if a, b := m.idx.Span(); !a.IsZero() {
			m.rangeNm += " · " + spanText(a, b.AddDate(0, 0, 1), m.cfg.Loc)
		}
	}
	var keep []string
	for _, v := range m.drill {
		if !strings.HasPrefix(v, "day:") {
			keep = append(keep, v)
		}
	}
	m.drill = keep
	m.recompute()
}

// initialPeriod maps the command-line range onto a period.
func initialPeriod(cfg Config) periodState {
	switch {
	case cfg.All:
		return periodState{preset: presetIndex("all")}
	case cfg.Bounded:
		for _, id := range []string{"today", "7d", "30d", "90d"} {
			n := map[string]int{"today": 1, "7d": 7, "30d": 30, "90d": 90}[id]
			if cfg.DaysFlag == n {
				return periodState{preset: presetIndex(id)}
			}
		}
		to := cfg.Filter.To
		return periodState{preset: customPreset, from: to.AddDate(0, 0, -cfg.DaysFlag), to: to, label: cfg.RangeName}
	}
	return periodState{preset: customPreset, from: cfg.Filter.From, to: cfg.Filter.To, label: "Custom range"}
}

// pickerKey handles keys while the period picker is open.
func (m *model_) pickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	entries := pickerEntries()
	switch k.String() {
	case "esc":
		m.picker = false
	case "up":
		m.pickSel = max(0, m.pickSel-1)
	case "down":
		m.pickSel = min(len(entries)-1, m.pickSel+1)
	case "enter":
		if strings.TrimSpace(m.pickIn) == "" {
			m.setPeriod(entries[m.pickSel].p)
			m.picker = false
			break
		}
		p, err := parsePeriod(m.pickIn, time.Now(), m.cfg.Loc)
		if err != nil {
			m.pickErr = err.Error()
			break
		}
		m.setPeriod(p)
		m.picker = false
	case "backspace":
		if r := []rune(m.pickIn); len(r) > 0 {
			m.pickIn = string(r[:len(r)-1])
		}
		m.pickErr = ""
	default:
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			m.pickIn += string(k.Runes)
			if k.Type == tea.KeySpace {
				m.pickIn += " "
			}
			m.pickErr = ""
		}
	}
	return m, nil
}

// drillDown narrows the whole view to the selected row.
func (m *model_) drillDown() {
	gs := m.groups()
	if !m.isTable() || m.cursor[m.tab] >= len(gs) {
		return
	}
	g := gs[m.cursor[m.tab]]
	switch m.tab {
	case tabDaily:
		d, err := time.ParseInLocation("2006-01-02", g.Key, m.cfg.Loc)
		if err != nil {
			return
		}
		m.filter.From, m.filter.To, m.rangeNm = d, d.AddDate(0, 0, 1), g.Key
		var keep []string
		for _, v := range m.drill {
			if !strings.HasPrefix(v, "day:") {
				keep = append(keep, v)
			}
		}
		m.drill = append(keep, "day:"+g.Key)
	case tabProviders:
		for i, id := range m.provList() {
			if id == g.Key {
				m.provIdx = i + 1
			}
		}
		m.drill = append(m.drill, "provider:"+g.Key)
	case tabModels:
		m.filter.Models = []string{strings.TrimPrefix(g.Label, "")}
		m.drill = append(m.drill, "model:"+g.Label)
	case tabProjects:
		m.filter.Projects = []string{g.Key}
		m.drill = append(m.drill, "project:"+g.Label)
	default:
		return // a session is already the finest level
	}
	m.tab = tabOverview
	m.recompute()
}

func (m *model_) ctx() *render.Ctx { return m.cx }

func (m *model_) filterChips() []string {
	var p []string
	if m.provIdx > 0 {
		p = append(p, "provider: "+m.provList()[m.provIdx-1])
	}
	for _, d := range m.drill {
		if !strings.HasPrefix(d, "provider:") && !strings.HasPrefix(d, "day:") {
			k, v, _ := strings.Cut(d, ":")
			p = append(p, k+": "+v)
		}
	}
	if m.filter.Text != "" {
		p = append(p, "search: "+m.filter.Text)
	}
	return p
}

func (m *model_) filterText() string { return strings.Join(m.filterChips(), " › ") }

// ---- geometry ------------------------------------------------------------

type geom struct {
	chartH   int // rows of the Daily chart panel (0 = none)
	tableTop int // screen row of the table panel's top border
	tableH   int // rows of the table panel
	tableW   int
	vis      int // data rows visible
	detail   int // 0 none, 1 right, 2 below
	detailW  int
	detailH  int
}

func (m *model_) geometry() geom {
	var g geom
	W, H := m.width, m.bodyH()
	g.tableW = W
	remaining := H
	if m.tab == tabDaily && H >= 16 {
		g.chartH = 9
		remaining -= g.chartH
	}
	switch {
	case m.tab != tabDaily && W >= 110:
		g.detail, g.detailW = 1, min(48, W/3)
		g.tableW = W - g.detailW - 1
	case m.tab != tabDaily && remaining >= 20:
		g.detail, g.detailH = 2, 11
		remaining -= g.detailH
	}
	g.tableH = remaining
	if g.detail != 2 && m.sum != nil { // do not stretch a short list over the whole screen
		floor := 6
		if g.detail == 1 {
			floor = 15 // match the side panel
		}
		g.tableH = min(remaining, max(len(m.groups())+3, floor))
	}
	g.tableTop = bodyTop + g.chartH
	g.vis = max(1, g.tableH-3) // border, header, border
	return g
}

// ---- view ----------------------------------------------------------------

func (m *model_) View() string {
	t := m.cfg.Theme
	if m.width == 0 {
		return ""
	}
	if m.width < minW || m.height < minH {
		return fmt.Sprintf("Terminal too small (%dx%d).\nNeed at least %dx%d.\nPress q to quit.", m.width, m.height, minW, minH)
	}
	if m.loading {
		return m.loadingView()
	}
	W := m.width
	tabRows, _ := m.tabLayout()
	lines := []string{m.headerLine(), tabRows[0], tabRows[1]}
	var body []string
	switch {
	case m.picker:
		body = m.pickerView()
	case m.help:
		body = m.helpView()
	case m.isTable():
		body = m.tableView()
	default:
		body = m.scrollView()
	}
	h := m.bodyH()
	for len(body) < h {
		body = append(body, "")
	}
	lines = append(lines, body[:h]...)
	lines = append(lines, m.footer()...)
	for i := range lines {
		lines[i] = t.Trunc(lines[i], W)
	}
	return strings.Join(lines, "\n")
}

const brand = "gauge·grain"

// pickerChip marks the "pick a period" button in headerLayout spans.
const pickerChip = -2

type chipSpan struct {
	x0, x1 int
	preset int
}

// headerLayout builds the top row and where each clickable chip sits.
// It is pure so the renderer and the mouse handler share it.
func (m *model_) headerLayout() (string, []chipSpan) {
	t := m.cfg.Theme
	var sb strings.Builder
	x := 0
	put := func(s string) { sb.WriteString(s); x += render.Width(s) }
	var spans []chipSpan
	chip := func(label string, active bool, preset int) {
		start := x
		if active {
			put(t.Chip(label, render.CSelBG, true))
		} else {
			put(t.Dim(" " + label + " "))
		}
		spans = append(spans, chipSpan{start, x, preset})
	}
	put(" " + t.FG(render.CAccent, t.Bold(brand)) + "  " + t.Dim("Period "))
	dayDrill := false
	for _, d := range m.drill {
		dayDrill = dayDrill || strings.HasPrefix(d, "day:")
	}
	chips := chipPresets()
	onChip := false
	for i, pi := range chips {
		active := !dayDrill && m.per.preset == pi
		onChip = onChip || active
		label := presets[pi].chip
		if active && m.per.shift > 0 {
			label += fmt.Sprintf(" ◂%d", m.per.shift)
		}
		chip(label, active, pi)
		if i < len(chips)-1 {
			put(t.Dim("│"))
		}
	}
	put(t.Dim("│"))
	switch {
	case dayDrill:
		chip(m.rangeNm, true, pickerChip)
	case !onChip:
		chip(m.per.name(), true, pickerChip) // calendar or custom period
	default:
		chip("Pick…", false, pickerChip)
	}
	if !dayDrill {
		if d := spanText(m.filter.From, m.filter.To, m.cfg.Loc); d != "" {
			put("  " + t.Dim(d))
		} else if m.idx != nil {
			if a, b := m.idx.Span(); !a.IsZero() {
				put("  " + t.Dim(spanText(a, b.AddDate(0, 0, 1), m.cfg.Loc)))
			}
		}
	}
	left := sb.String()
	var chipsR []string
	for _, c := range m.filterChips() {
		chipsR = append(chipsR, t.Chip(c, 130, false))
	}
	right := strings.Join(chipsR, " ")
	gap := m.width - render.Width(left) - render.Width(right) - 1
	if gap < 1 {
		return left, spans
	}
	return left + strings.Repeat(" ", gap) + right, spans
}

func (m *model_) headerLine() string {
	s, _ := m.headerLayout()
	return s
}

func (m *model_) tabLabels() []string {
	names := tabNames
	if m.width < 90 {
		names = tabShort
	}
	out := make([]string, numTabs)
	for i := range out {
		out[i] = fmt.Sprintf("%d %s", i+1, names[i])
		if i == tabNotes && m.warn > 0 {
			out[i] += fmt.Sprintf(" ⚠%d", m.warn)
			if m.cfg.Theme.ASCII {
				out[i] = fmt.Sprintf("%d %s !%d", i+1, names[i], m.warn)
			}
		}
	}
	return out
}

// tabLayout renders the tab row and the rule under it, whose segment under
// the active tab is highlighted, and returns each tab's clickable span.
func (m *model_) tabLayout() ([]string, [][2]int) {
	t := m.cfg.Theme
	labels := m.tabLabels()
	total := 1
	for _, l := range labels {
		total += render.Width(l) + 4
	}
	rule, heavy := "─", "━"
	if t.ASCII {
		rule, heavy = "-", "="
	}
	if total > m.width { // very narrow: only the active tab
		l := fmt.Sprintf("‹ %d/%d %s ›", m.tab+1, numTabs, tabNames[m.tab])
		return []string{" " + t.Bold(l), t.FG(render.CMuted-4, strings.Repeat(rule, m.width))}, make([][2]int, numTabs)
	}
	var row strings.Builder
	spans := make([][2]int, numTabs)
	x := 1
	row.WriteString(" ")
	for i, l := range labels {
		w := render.Width(l) + 2
		spans[i] = [2]int{x, x + w}
		txt := " " + l + " "
		switch {
		case i == m.tab:
			row.WriteString(t.FG(255, t.Bold(txt)))
		case i == tabNotes && m.warn > 0:
			row.WriteString(t.Warn(txt))
		default:
			row.WriteString(t.Dim(txt))
		}
		row.WriteString("  ")
		x += w + 2
	}
	var ru strings.Builder
	for c := 0; c < m.width; c++ {
		if c >= spans[m.tab][0] && c < spans[m.tab][1] {
			ru.WriteString(t.FG(render.CAccent, heavy))
		} else {
			ru.WriteString(t.FG(render.CMuted-4, rule))
		}
	}
	return []string{row.String(), ru.String()}, spans
}

func (m *model_) tabBar() string {
	r, _ := m.tabLayout()
	return r[0]
}

// ---- overview & notes (scrolling text views) -------------------------------

// scrollLines renders the Overview or Notes. The result is cached per
// (data version, width): scrolling and mouse events re-use it, so only a
// real change in filters, period or size pays for a rebuild.
func (m *model_) scrollLines() []string {
	if m.sum == nil {
		return nil
	}
	c := &m.cache[m.tab]
	if c.lines != nil && c.ver == m.ver && c.w == m.width {
		return c.lines
	}
	if m.tab == tabNotes {
		c.lines = m.notesLines()
	} else {
		c.lines = m.overviewLines()
	}
	c.ver, c.w = m.ver, m.width
	return c.lines
}

func (m *model_) scrollView() []string {
	lines := m.scrollLines()
	h := m.bodyH()
	off := min(m.offset[m.tab], max(0, len(lines)-h))
	m.offset[m.tab] = off
	end := min(len(lines), off+h)
	out := append([]string(nil), lines[off:end]...)
	if len(lines) > h { // scroll indicator on the last visible line
		t := m.cfg.Theme
		mark := t.Dim(fmt.Sprintf(" ↕ %d–%d of %d lines ", off+1, end, len(lines)))
		if len(out) > 0 {
			out[len(out)-1] = t.Pad(out[len(out)-1], m.width-render.Width(mark)) + mark
		}
	}
	return out
}

func (m *model_) overviewLines() []string {
	t, W, c, s := m.cfg.Theme, m.width, m.ctx(), m.sum
	tot := s.Total
	var out []string

	// KPI cards
	cols := 4
	if W < 88 {
		cols = 2
	}
	gap := 1
	cw := (W - gap*(cols-1)) / cols
	tokDelta := t.Dim("all providers, same definition")
	costDelta := t.Dim("API-equivalent · not your bill")
	if s.Prev != nil {
		if ch, ok := agg.Change(float64(tot.Tokens()), float64(s.Prev.Tokens())); ok {
			tokDelta = t.Arrow(ch) + t.Dim(" vs "+m.prevSpan())
		}
		if ch, ok := agg.Change(tot.Cost, s.Prev.Cost); ok && tot.Complete() {
			costDelta = t.Arrow(ch) + t.Dim(" vs "+m.prevSpan()+" · estimate")
		}
	}
	costVal := t.FG(render.CGood, t.Bold(render.Cost(tot.Cost)))
	switch {
	case tot.Tokens() == 0:
		costVal = t.Bold("—")
	case tot.PricedTok == 0:
		costVal, costDelta = t.Warn(t.Bold("unpriced")), t.Dim("no known price for these models")
	case !tot.Complete():
		costVal = t.Warn(t.Bold("≥ " + render.Cost(tot.Cost)))
		costDelta = t.Warn(fmt.Sprintf("partial · %s unpriced", render.Tokens(tot.UnpricedTok)))
	}
	cacheVal, cacheNote := t.Bold("n/a"), t.Dim("not reported by these sources")
	if sh, ok := tot.CacheShare(); ok && tot.Has&model.HasCacheRead != 0 {
		cacheVal, cacheNote = t.FG(render.CCacheRead, t.Bold(render.Pct(sh))), t.Dim("of input-side tokens")
	}
	cards := [][]string{
		t.Panel("Tokens", []string{t.FG(render.CAccent, t.Bold(render.Tokens(tot.Tokens()))), tokDelta}, cw, render.CAccent),
		t.Panel("Est. cost", []string{costVal, costDelta}, cw, render.CGood),
		t.Panel("Sessions", []string{t.Bold(render.Num(len(tot.Sessions))), t.Dim(render.Num(tot.Events) + " API calls")}, cw, render.CInput),
		t.Panel("Cache hits", []string{cacheVal, cacheNote}, cw, render.CCacheRead),
	}
	for i := 0; i < len(cards); i += cols {
		end := min(len(cards), i+cols)
		ws := make([]int, end-i)
		for j := range ws {
			ws[j] = cw
		}
		out = append(out, t.Side(ws, gap, cards[i:end]...)...)
	}

	// sources + notes pointer
	out = append(out, " "+m.sourcesLine(), " "+render.NoteLine(t, c, "press n"))

	if tot.Tokens() == 0 {
		out = append(out, "", t.Dim("  No usage for these filters. Press c to clear them or d for a wider range."))
		return out
	}

	// trend + token mix
	var trend, mix []string
	wide := W >= 100
	tw, mw := W, W
	if wide {
		tw = W * 11 / 20
		mw = W - tw - 1
	}
	trend = t.Panel("Daily tokens", m.trendLines(tw-4, 6, -1), tw, render.CAccent)
	mix = t.Panel("Token mix", m.mixLines(mw-4), mw, render.CCacheWrite)
	if wide {
		out = append(out, t.Side([]int{tw, mw}, 1, trend, mix)...)
	} else {
		out = append(out, trend...)
		out = append(out, mix...)
	}

	// providers + models
	pw, mdw := W, W
	if wide {
		pw = W * 2 / 5
		mdw = W - pw - 1
	}
	prov := t.Panel("Providers", miniRows(t, s.ByProvider, tot.Tokens(), pw-4, 4), pw, render.CAccent)
	mod := t.Panel("Models", miniRows(t, s.ByModel, tot.Tokens(), mdw-4, 5), mdw, render.CAccent)
	if wide {
		out = append(out, t.Side([]int{pw, mdw}, 1, prov, mod)...)
	} else {
		out = append(out, prov...)
		out = append(out, mod...)
	}
	out = append(out, t.Panel("Projects", miniRows(t, s.ByProject, tot.Tokens(), W-4, 5), W, render.CAccent)...)
	return out
}

// prevSpan names the comparison window, e.g. "31 Aug – 29 Sep 2026".
func (m *model_) prevSpan() string {
	f, to := m.filter.From, m.filter.To
	if f.IsZero() || to.IsZero() {
		return "previous period"
	}
	pf, pt := m.filter.PrevFrom, m.filter.PrevTo
	if pf.IsZero() {
		pf, pt = f.Add(-to.Sub(f)), f
	}
	return spanShort(pf, pt, m.cfg.Loc)
}

func (m *model_) sourcesLine() string {
	t := m.cfg.Theme
	var parts []string
	for _, id := range []string{"claude", "codex", "gemini"} {
		d, ok := m.cfg.Detect[id]
		if !ok {
			continue
		}
		name := providerName(id)
		_, used := m.sum.Total.Providers[id]
		switch {
		case used:
			parts = append(parts, t.Dot(id)+" "+name)
		case d.Status == providers.Unsupported:
			parts = append(parts, t.Warn("◐ "+name+" not parsed"))
		case d.Status == providers.Ready:
			parts = append(parts, t.Dim("○ "+name+" (no usage in range)"))
		default:
			parts = append(parts, t.Dim("○ "+name+" not found"))
		}
	}
	return t.Dim("Sources ") + strings.Join(parts, "   ")
}

func providerName(id string) string {
	if p := providers.ByID(id); p != nil {
		return p.Name()
	}
	return id
}

func (m *model_) trendLines(w, h, sel int) []string {
	t, days := m.cfg.Theme, m.sum.ByDay
	if len(days) == 0 {
		return []string{t.Dim("no usage in this range")}
	}
	vals := make([]float64, len(days))
	peak := days[0]
	var active int
	for i, d := range days {
		vals[i] = float64(d.Tokens())
		if d.Tokens() > peak.Tokens() {
			peak = d
		}
		if d.Tokens() > 0 {
			active++
		}
	}
	out := t.VChart(vals, w, h, sel, render.CAccent)
	first, last := days[0].Key, days[len(days)-1].Key
	axis := first + strings.Repeat(" ", max(1, w-len(first)-len(last))) + last
	out = append(out, t.Dim(axis))
	out = append(out, t.Dim(fmt.Sprintf("peak %s %s · %d/%d days active", peak.Key, render.Tokens(peak.Tokens()), active, len(days))))
	return out
}

func (m *model_) mixLines(w int) []string {
	t, g := m.cfg.Theme, m.sum.Total
	tot := float64(g.Tokens())
	bw := max(0, w-30)
	row := func(name string, color int, n int64, ok bool) string {
		if !ok {
			return t.FG(color, "■ ") + t.Dim(t.Pad(name, 11)+t.PadL("n/a", 8))
		}
		l := t.FG(color, "■ ") + t.Pad(name, 11) + t.PadL(render.Tokens(n), 8) + " " + t.PadL(render.Pct(float64(n)/tot), 6)
		if bw >= 4 {
			l += " " + t.ColBar(float64(n)/tot, bw, color)
		}
		return l
	}
	out := []string{
		row("input", render.CInput, g.Input, true),
		row("cache read", render.CCacheRead, g.CacheRead, g.Has&model.HasCacheRead != 0),
		row("cache write", render.CCacheWrite, g.CacheWrite, g.Has&model.HasCacheWrite != 0),
		row("output", render.COutput, g.Output, true),
	}
	if g.Has&model.HasReasoning != 0 {
		out = append(out, t.Dim(fmt.Sprintf("  ↳ %s of output is reasoning", render.Tokens(g.Reasoning))))
	}
	out = append(out, "", t.Dim("input = uncached prompt · cache read = reused"), t.Dim("prompt (cheap) · cache write = stored (pricier)"))
	return out
}

// miniRows is a compact ranked list for dashboard panels.
func miniRows(t render.Theme, gs []*agg.Group, total int64, w, n int) []string {
	if len(gs) == 0 {
		return []string{t.Dim("no data")}
	}
	showCost := w >= 38
	bw := max(0, min(14, w-44))
	lw := max(8, w-8-8-2*2-boolW(showCost, 10)-boolW(bw > 0, bw+1)-2)
	var out []string
	for i, g := range gs {
		if i >= n {
			out = append(out, t.Dim(fmt.Sprintf("… %d more", len(gs)-n)))
			break
		}
		color, dot := render.CAccent, "  "
		if len(g.Providers) == 1 {
			for p := range g.Providers {
				color, dot = render.ProviderColor(p), t.Dot(p)+" "
			}
		}
		share := 0.0
		if total > 0 {
			share = float64(g.Tokens()) / float64(total)
		}
		label := g.Label
		if !g.Complete() {
			label = t.Pad(label, max(4, lw-1)) + t.Warn("⚠")
		}
		l := dot + t.Pad(label, lw) + " " + t.PadL(render.Tokens(g.Tokens()), 7) + " " + t.PadL(render.Pct(share), 6)
		if showCost {
			l += " " + t.PadL(costText(t, g), 9)
		}
		if bw > 0 {
			l += " " + t.ColBar(share, bw, color)
		}
		out = append(out, l)
	}
	return out
}

func boolW(b bool, n int) int {
	if b {
		return n
	}
	return 0
}

func costText(t render.Theme, g *agg.Group) string {
	switch {
	case g.Tokens() == 0:
		return "—"
	case g.PricedTok == 0:
		return t.Warn("unpriced")
	case !g.Complete():
		return t.Warn("≥ " + render.Cost(g.Cost))
	}
	return render.Cost(g.Cost)
}

func (m *model_) notesLines() []string {
	t, W, c := m.cfg.Theme, m.width, m.ctx()
	var out []string

	var nl []string
	warn, info := "⚠ ", "ℹ "
	if t.ASCII {
		warn, info = "! ", "i "
	}
	inner := W - 4
	for _, n := range render.NoteItems(c) {
		mark := t.Dim(info)
		if n.Warn {
			mark = t.Warn(warn)
		}
		for i, l := range wrap(n.Text, max(20, inner-2)) {
			pre := "  "
			if i == 0 {
				pre = mark
			}
			if n.Warn {
				nl = append(nl, pre+l)
			} else {
				nl = append(nl, pre+t.Dim(l))
			}
		}
	}
	w, _ := render.NoteCounts(c)
	title := "Notes · how far to trust these numbers"
	color := render.CAccent
	if w > 0 {
		title, color = fmt.Sprintf("Notes · %d need attention", w), render.CWarn
	}
	out = append(out, t.Panel(title, nl, W, color)...)

	// sources
	var sl []string
	for _, id := range []string{"claude", "codex", "gemini"} {
		d, ok := m.cfg.Detect[id]
		if !ok {
			continue
		}
		status := d.Status.String()
		switch d.Status {
		case providers.Ready:
			status = t.FG(render.CGood, "ready")
		case providers.Unsupported:
			status = t.Warn(status)
		default:
			status = t.Dim(status)
		}
		files := ""
		if n := m.res.Files[id]; n > 0 {
			files = t.Dim(fmt.Sprintf(" · %s files", render.Num(n)))
		}
		sl = append(sl, t.Dot(id)+" "+t.Bold(t.Pad(providerName(id), 14))+t.Pad(status, 24)+files)
		for _, dir := range d.Dirs {
			sl = append(sl, "    "+t.Dim(dir))
		}
		if len(d.Dirs) == 0 {
			sl = append(sl, "    "+t.Dim(d.Detail))
		}
	}
	sl = append(sl, "", t.Dim(fmt.Sprintf("scan: %s files (%d from cache, %d parsed) → %s API calls after de-duplication, in %s",
		render.Num(m.res.Cached+m.res.Parsed), m.res.Cached, m.res.Parsed, render.Num(len(m.events)), m.scanTook.Round(100*time.Millisecond))))
	out = append(out, t.Panel("Sources · only local files are read", sl, W, render.CAccent)...)

	// prices
	var pl []string
	for _, s := range m.cfg.Prices.Sources {
		pl = append(pl, t.Bold(s.Name)+t.Dim(" · updated "+s.Updated))
		pl = append(pl, "  "+t.Dim(s.URL))
	}
	pl = append(pl, "", t.Dim("Unknown models are never guessed: they show as unpriced. Override with prices.json (gg prices)."))
	out = append(out, t.Panel("Prices · list prices, offline", pl, W, render.CAccent)...)
	return out
}

func wrap(s string, w int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		if cur != "" && render.Width(cur)+1+render.Width(word) > w {
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
	return lines
}

// ---- table tabs -----------------------------------------------------------

func (m *model_) tableView() []string {
	t, W := m.cfg.Theme, m.width
	g := m.geometry()
	gs := m.groups()
	var out []string

	if g.chartH > 0 {
		vals := make([]float64, len(m.sum.ByDay))
		for i, d := range m.sum.ByDay {
			vals[i] = float64(d.Tokens())
		}
		sel := -1
		if len(gs) > 0 {
			sel = len(vals) - 1 - m.cursor[m.tab]
		}
		title := "Daily tokens"
		if sel >= 0 && sel < len(m.sum.ByDay) {
			d := m.sum.ByDay[sel]
			title = fmt.Sprintf("%s · %s · %s", d.Label, render.Tokens(d.Tokens()), costText(render.Theme{ASCII: t.ASCII}, d))
		}
		lines := t.VChart(vals, W-4, g.chartH-3, sel, render.CAccent)
		days := m.sum.ByDay
		if len(days) > 0 {
			f, l := days[0].Key, days[len(days)-1].Key
			lines = append(lines, t.Dim(f+strings.Repeat(" ", max(1, W-4-len(f)-len(l)))+l))
		}
		out = append(out, t.Panel(title, lines, W, render.CAccent)...)
	}

	total := m.sum.Total.Tokens()
	tw := g.tableW
	hdr, rows := render.TableRowsOpt(t, gs, total, tw-4, tabLabel[m.tab], true, m.tab == tabDaily)
	inner := []string{strings.TrimPrefix(hdr, "  ")}
	off := min(m.offset[m.tab], max(0, len(rows)-g.vis))
	m.offset[m.tab] = off
	for i := off; i < min(len(rows), off+g.vis); i++ {
		r := strings.TrimPrefix(rows[i], "  ")
		if i == m.cursor[m.tab] {
			r = t.Select(r, tw-4)
			if !t.Color {
				r = ">" + strings.TrimPrefix(render.Strip(r), " ")
			}
		}
		inner = append(inner, r)
	}
	if len(gs) == 0 {
		inner = append(inner, "", t.Dim("  no data for the current filters — c clears them, d widens the range"))
	}
	title := fmt.Sprintf("%s · %d", tabNames[m.tab], len(gs))
	if len(gs) > g.vis {
		title += fmt.Sprintf(" · %d–%d", off+1, min(len(gs), off+g.vis))
	}
	if m.sort == byCost && m.tab != tabDaily {
		title += " · by cost"
	}
	sbStart, sbLen := -1, 0
	if len(gs) > g.vis { // scrollbar thumb along the right border
		sbLen = max(1, g.vis*g.vis/len(gs))
		sbStart = 1 + (g.vis-sbLen)*off/max(1, len(gs)-g.vis)
	}
	table := t.PanelS(title, padTo(inner, g.tableH-2), tw, render.CAccent, sbStart, sbLen)

	switch g.detail {
	case 1:
		out = append(out, t.Side([]int{tw, g.detailW}, 1, table, m.detailPanel(gs, g.detailW, g.tableH))...)
	case 2:
		out = append(out, table...)
		out = append(out, m.detailPanel(gs, W, g.detailH)...)
	default:
		out = append(out, table...)
	}
	return out
}

func padTo(l []string, n int) []string {
	for len(l) < n {
		l = append(l, "")
	}
	return l
}

func (m *model_) detailPanel(gs []*agg.Group, w, h int) []string {
	t := m.cfg.Theme
	if len(gs) == 0 || m.cursor[m.tab] >= len(gs) {
		return t.Panel("Details", padTo(nil, h-2), w, render.CMuted)
	}
	g := gs[m.cursor[m.tab]]
	lines := render.Detail(t, g, w-4)
	switch m.tab {
	case tabSessions:
		lines = append(lines, t.Dim(g.Last.In(m.cfg.Loc).Format("2006-01-02 15:04")))
	case tabDaily, tabProviders, tabModels, tabProjects:
		lines = append(lines, "", t.Dim("Enter filters everything to this "+strings.ToLower(tabLabel[m.tab])))
	}
	return t.Panel("Details", padTo(lines, h-2)[:max(0, h-2)], w, render.ProviderColorOr(g))
}

// ---- footer, help, loading -------------------------------------------------

func (m *model_) footer() []string {
	t, W := m.cfg.Theme, m.width
	if m.search {
		prompt := t.Chip("search", render.CSelBG, true) + " " + m.input + t.FG(render.CAccent, "█")
		return []string{t.Dim(m.status()), t.Pad(prompt+t.Dim("   Enter apply · Esc cancel · matches model, project or session"), W)}
	}
	type hint struct{ key, what string }
	all := []hint{{"←→", "view"}, {"↑↓", "move"}, {"⏎", "drill"}, {"esc", "back"}, {"t", "period"}, {"[ ]", "shift"}, {"p", "provider"}, {"/", "search"}, {"s", "sort"}, {"n", "notes"}, {"?", "help"}, {"q", "quit"}}
	if !m.isTable() {
		all[1].what, all[2] = "scroll", hint{"r", "rescan"}
	}
	var parts []string
	used := 0
	for i, h := range all {
		p := t.Bold(h.key) + " " + t.Dim(h.what)
		need := render.Width(h.key) + 1 + len(h.what) + 3
		mandatory := h.key == "?" || h.key == "q"
		reserve := 0
		if !mandatory {
			reserve = 13 // always keep room for "? help  q quit"
		}
		if used+need+reserve > W && !(mandatory && used+need <= W) {
			_ = i
			continue
		}
		parts = append(parts, p)
		used += need
	}
	return []string{t.Dim(m.status()), " " + strings.Join(parts, t.Dim(" · "))}
}

func (m *model_) status() string {
	t := m.cfg.Theme
	s := fmt.Sprintf(" %s · %s calls", m.rangeNm, render.Num(m.sum.Total.Events))
	if m.sort == byCost {
		s += " · sorted by cost"
	}
	if m.cancelled {
		s += " · " + "PARTIAL (scan cancelled)"
		return t.Warn(s)
	}
	_ = t
	return s
}

func (m *model_) helpView() []string {
	t, W := m.cfg.Theme, m.width
	rows := [][2]string{
		{"← → · Tab · 1-7", "switch view"},
		{"↑ ↓ · j k · PgUp PgDn · g G", "move or scroll"},
		{"Enter  (or click a selected row)", "drill into the selected day / provider / model / project"},
		{"Esc · Backspace", "undo the last drill-down → clear search → back to Overview"},
		{"t  (or click Pick…)", "choose a period: presets, or type 2026-10 · 14d · 2026-10-01..2026-10-15"},
		{"d · D  (or click a chip)", "next / previous quick period: Today, 7d, 30d, 90d, All"},
		{"[ · ]  ·  .", "move the period earlier / later by its own length · back to now"},
		{"p", "cycle the provider filter"},
		{"/", "search model, project or session text"},
		{"s", "sort by tokens or by estimated cost"},
		{"n", "notes: estimates, gaps, unpriced models, sources, prices"},
		{"c", "clear every filter"},
		{"r", "rescan files (unchanged files come from the cache)"},
		{"mouse wheel", "scroll"},
		{"q · Ctrl-C", "quit"},
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, t.Bold(t.Pad(r[0], 34))+t.Dim(r[1]))
	}
	lines = append(lines, "", t.Dim("Any key closes this help."))
	w := min(W, 100)
	p := t.Panel("Keys", lines, w, render.CAccent)
	pad := strings.Repeat(" ", (W-w)/2)
	for i := range p {
		p[i] = pad + p[i]
	}
	return append([]string{""}, p...)
}

func (m *model_) loadingView() string {
	t := m.cfg.Theme
	p := m.prog
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if t.ASCII {
		frames = []string{"|", "/", "-", "\\"}
	}
	w := max(30, min(64, m.width-4))
	bw := w - 12
	lines := []string{
		t.FG(render.CAccent, frames[m.frame%len(frames)]) + " Reading local usage files",
		"",
		t.ColBar(frac, bw, render.CAccent) + fmt.Sprintf(" %3.0f%%", frac*100),
		t.Dim(fmt.Sprintf("%s of %s files · %s · %s", render.Num(p.Done), render.Num(p.Total), humanBytes(p.Bytes), time.Since(m.started).Round(100*time.Millisecond))),
		"",
		t.Dim("Nothing leaves your machine. The first scan of a big history"),
		t.Dim("takes a while; later runs only read what changed."),
		"",
		t.Bold("Esc") + t.Dim(" cancel cleanly (finished files stay cached) · ") + t.Bold("Ctrl-C") + t.Dim(" quit"),
	}
	box := t.Panel("gauge-and-grain", lines, w, render.CAccent)
	var out []string
	top := max(0, (m.height-len(box))/2)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	pad := strings.Repeat(" ", max(0, (m.width-w)/2))
	for _, l := range box {
		out = append(out, t.Trunc(pad+l, m.width))
	}
	return strings.Join(out, "\n")
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

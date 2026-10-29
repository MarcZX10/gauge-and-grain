// Package tui is the interactive view. It reuses the report's renderers so
// the TUI and `gg report` can never disagree.
package tui

import (
	"context"
	"fmt"
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
)

var tabNames = []string{"Overview", "Daily", "Providers", "Models", "Projects", "Sessions"}
var tabShort = []string{"Ovw", "Day", "Prv", "Mdl", "Prj", "Ses"}

// ranges the `d` key cycles through (0 = all time).
var ranges = []int{7, 30, 90, 0}

type sortBy int

const (
	byTokens sortBy = iota
	byCost
)

type model_ struct {
	cfg    Config
	width  int
	height int

	// scanning
	loading   bool
	cancel    context.CancelFunc
	ch        chan tea.Msg
	prog      scan.Progress
	res       *scan.Result
	events    []model.Event
	cancelled bool

	// view state
	tab      int
	cursor   [6]int
	offset   [6]int
	filter   agg.Filter
	rangeIdx int
	provIdx  int // 0 = all
	drill    []string
	sum      *agg.Summary
	sort     sortBy
	search   bool
	input    string
	help     bool
	rangeNm  string
}

type progressMsg scan.Progress
type doneMsg *scan.Result
type startedMsg struct {
	ch     chan tea.Msg
	cancel context.CancelFunc
}

// Run starts the TUI and blocks until the user quits.
func Run(cfg Config) error {
	m := &model_{cfg: cfg, filter: cfg.Filter, rangeNm: cfg.RangeName, loading: true}
	m.rangeIdx = len(ranges) - 1
	for i, d := range ranges {
		if (d == cfg.DaysFlag && !cfg.All) || (d == 0 && cfg.All) {
			m.rangeIdx = i
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	if m.cancel != nil {
		m.cancel()
	}
	return err
}

func (m *model_) Init() tea.Cmd { return m.startScan() }

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

func (m *model_) recompute() {
	if m.res == nil {
		return
	}
	f := m.filter
	if m.provIdx > 0 {
		f.Providers = []string{m.provList()[m.provIdx-1]}
	}
	m.sum = agg.Summarize(m.events, f, m.cfg.Prices, m.cfg.Loc)
	if m.sort == byCost {
		for _, gs := range [][]*agg.Group{m.sum.ByProvider, m.sum.ByModel, m.sum.ByProject, m.sum.BySession} {
			sortByCost(gs)
		}
	}
	for i := range m.cursor {
		m.cursor[i], m.offset[i] = 0, 0
	}
}

func (m *model_) provList() []string {
	var out []string
	for _, p := range m.cfg.Providers {
		out = append(out, p.ID())
	}
	return out
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
	case doneMsg:
		m.res = (*scan.Result)(msg)
		m.events = m.res.Events
		m.cancelled = m.res.Cancelled
		m.loading = false
		m.recompute()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model_) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.loading {
		switch s {
		case "esc", "q":
			// Cancel cleanly: the scan stops, finished files stay cached
			// and whatever was read so far is shown as partial.
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
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
			if len(m.input) > 0 {
				r := []rune(m.input)
				m.input = string(r[:len(r)-1])
			}
		default:
			if len(k.Runes) > 0 && k.Type == tea.KeyRunes {
				m.input += string(k.Runes)
			}
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	n := m.rowCount()
	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % len(tabNames)
	case "shift+tab", "left", "h":
		m.tab = (m.tab + len(tabNames) - 1) % len(tabNames)
	case "1", "2", "3", "4", "5", "6":
		m.tab = int(s[0] - '1')
	case "o":
		m.tab = 0
	case "up", "k":
		m.move(-1, n)
	case "down", "j":
		m.move(1, n)
	case "pgup":
		m.move(-m.bodyH()+1, n)
	case "pgdown":
		m.move(m.bodyH()-1, n)
	case "home", "g":
		m.cursor[m.tab], m.offset[m.tab] = 0, 0
	case "end", "G":
		m.move(n, n)
	case "enter":
		m.drillDown()
	case "esc", "backspace":
		m.back()
	case "d":
		m.cycleRange()
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
		m.applyRange()
	case "r", "R":
		m.loading, m.cancelled, m.prog = true, false, scan.Progress{}
		return m, m.startScan()
	}
	return m, nil
}

// back undoes the last drill-down, or returns to the overview.
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
			m.applyRange()
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
	m.tab = 0
}

func (m *model_) cycleRange() {
	m.rangeIdx = (m.rangeIdx + 1) % len(ranges)
	m.applyRange()
}

func (m *model_) applyRange() {
	d := ranges[m.rangeIdx]
	now := time.Now().In(m.cfg.Loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.cfg.Loc)
	if d == 0 {
		m.filter.From, m.filter.To, m.rangeNm = time.Time{}, time.Time{}, "all time"
	} else {
		m.filter.From, m.filter.To = start.AddDate(0, 0, -(d-1)), start.AddDate(0, 0, 1)
		m.rangeNm = render.RangeLabel(d)
	}
	m.drill = dropKind(m.drill, "day")
	m.recompute()
}

func dropKind(d []string, kind string) []string {
	var out []string
	for _, v := range d {
		if !strings.HasPrefix(v, kind+":") {
			out = append(out, v)
		}
	}
	return out
}

func (m *model_) groups() []*agg.Group {
	if m.sum == nil {
		return nil
	}
	switch m.tab {
	case 1:
		out := make([]*agg.Group, len(m.sum.ByDay))
		for i, g := range m.sum.ByDay {
			out[len(out)-1-i] = g
		}
		return out
	case 2:
		return m.sum.ByProvider
	case 3:
		return m.sum.ByModel
	case 4:
		return m.sum.ByProject
	case 5:
		return m.sum.BySession
	}
	return nil
}

func (m *model_) rowCount() int {
	if m.tab == 0 {
		return len(m.bodyLines())
	}
	return len(m.groups())
}

func (m *model_) bodyH() int { return max(1, m.height-5) } // header, tabs, rule, 2 footer lines

func (m *model_) move(d, n int) {
	if n == 0 {
		return
	}
	if m.tab == 0 { // overview scrolls as plain text
		m.offset[0] = max(0, min(m.offset[0]+d, max(0, n-m.bodyH())))
		return
	}
	c := max(0, min(m.cursor[m.tab]+d, n-1))
	m.cursor[m.tab] = c
	h := max(1, m.bodyH()-2)
	if c < m.offset[m.tab] {
		m.offset[m.tab] = c
	}
	if c >= m.offset[m.tab]+h {
		m.offset[m.tab] = c - h + 1
	}
}

// drillDown narrows the whole view to the selected row.
func (m *model_) drillDown() {
	gs := m.groups()
	if m.tab == 0 || m.cursor[m.tab] >= len(gs) {
		return
	}
	g := gs[m.cursor[m.tab]]
	switch m.tab {
	case 1:
		d, err := time.ParseInLocation("2006-01-02", g.Key, m.cfg.Loc)
		if err != nil {
			return
		}
		m.filter.From, m.filter.To, m.rangeNm = d, d.AddDate(0, 0, 1), g.Key
		m.drill = append(dropKind(m.drill, "day"), "day:"+g.Key)
	case 2:
		for i, id := range m.provList() {
			if id == g.Key {
				m.provIdx = i + 1
			}
		}
		m.drill = append(m.drill, "provider:"+g.Key)
	case 3:
		m.filter.Models = []string{g.Label}
		m.drill = append(m.drill, "model:"+g.Label)
	case 4:
		m.filter.Projects = []string{g.Key}
		m.drill = append(m.drill, "project:"+g.Label)
	default:
		return // a session is already the finest level
	}
	m.tab = 0
	m.recompute()
}

func sortByCost(gs []*agg.Group) {
	for i := 1; i < len(gs); i++ { // small insertion sort: stable, lists are short
		for j := i; j > 0 && gs[j].Cost > gs[j-1].Cost; j-- {
			gs[j], gs[j-1] = gs[j-1], gs[j]
		}
	}
}

func (m *model_) ctx() *render.Ctx {
	return &render.Ctx{Sum: m.sum, Detect: m.cfg.Detect, Files: m.res.Files, Stats: m.res.Stats,
		Cached: m.res.Cached, Parsed: m.res.Parsed, Duplicate: m.res.Duplicate, Errors: len(m.res.Errors),
		Cancelled: m.cancelled, Prices: m.cfg.Prices, RangeName: m.rangeNm, Filters: m.filterText()}
}

func (m *model_) filterText() string {
	var p []string
	if m.provIdx > 0 {
		p = append(p, "provider="+m.provList()[m.provIdx-1])
	}
	for _, d := range m.drill {
		if !strings.HasPrefix(d, "provider:") && !strings.HasPrefix(d, "day:") {
			p = append(p, strings.Replace(d, ":", "=", 1))
		}
	}
	if m.filter.Text != "" {
		p = append(p, "text~"+m.filter.Text)
	}
	return strings.Join(p, " › ")
}

// bodyLines is the Overview content (scrolls as text).
func (m *model_) bodyLines() []string {
	if m.sum == nil {
		return nil
	}
	t, w, c := m.cfg.Theme, m.width, m.ctx()
	var out []string
	add := func(l []string) {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, l...)
	}
	add(render.Overview(t, c, w))
	if m.sum.Total.Tokens() > 0 {
		add(render.TokenMix(t, c, w))
		add(render.Daily(t, c, w))
		add(render.Providers(t, c, w, 4))
		add(render.Models(t, c, w, 5))
	}
	add(render.Notes(t, c, w))
	// Truncate every line to the window so nothing wraps.
	for i := range out {
		if render.Width(out[i]) > w {
			out[i] = t.Trunc(out[i], w)
		}
	}
	return out
}

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
	var b strings.Builder
	b.WriteString(t.Trunc(strings.Join(render.Header(t, m.ctx(), m.width), ""), m.width) + "\n")
	b.WriteString(m.tabBar() + "\n")
	body := m.body()
	lines := strings.Split(body, "\n")
	h := m.bodyH()
	for len(lines) < h {
		lines = append(lines, "")
	}
	b.WriteString(strings.Join(lines[:h], "\n") + "\n")
	b.WriteString(t.Dim(strings.Repeat("─", m.width)) + "\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m *model_) tabBar() string {
	t := m.cfg.Theme
	names := tabNames
	if m.width < 64 {
		names = tabShort
	}
	var parts []string
	for i, n := range names {
		label := fmt.Sprintf("%d %s", i+1, n)
		if i == m.tab {
			parts = append(parts, t.Invert(" "+label+" "))
		} else {
			parts = append(parts, t.Dim(" "+label+" "))
		}
	}
	line := strings.Join(parts, "")
	if render.Width(line) > m.width { // very narrow: show the active tab only
		line = t.Invert(fmt.Sprintf(" %d/%d %s ", m.tab+1, len(tabNames), tabNames[m.tab]))
	}
	if !t.Color && len(parts) > 0 { // plain mode: mark the active tab with brackets
		parts = parts[:0]
		for i, n := range names {
			if i == m.tab {
				parts = append(parts, fmt.Sprintf("[%d %s]", i+1, n))
			} else {
				parts = append(parts, fmt.Sprintf(" %d %s ", i+1, n))
			}
		}
		line = strings.Join(parts, "")
		if len(line) > m.width {
			line = fmt.Sprintf("[%d/%d %s]", m.tab+1, len(tabNames), tabNames[m.tab])
		}
	}
	return line
}

func (m *model_) body() string {
	t := m.cfg.Theme
	if m.help {
		return m.helpView()
	}
	if m.tab == 0 {
		lines := m.bodyLines()
		off := min(m.offset[0], max(0, len(lines)-m.bodyH()))
		return strings.Join(lines[off:min(len(lines), off+m.bodyH())], "\n")
	}
	gs := m.groups()
	if len(gs) == 0 {
		return t.Dim("  no data for the current filters — press c to clear them, d to widen the range")
	}
	label := map[int]string{1: "day", 2: "provider", 3: "model", 4: "project", 5: "session"}[m.tab]
	total := m.sum.Total.Tokens()
	hdr, rows := render.TableRows(t, gs, total, m.width, label, true)
	var out []string
	out = append(out, hdr)
	h := max(1, m.bodyH()-2)
	off := min(m.offset[m.tab], max(0, len(rows)-h))
	for i := off; i < min(len(rows), off+h); i++ {
		r := rows[i]
		if i == m.cursor[m.tab] {
			if t.Color {
				r = t.Invert(t.Pad(render.Strip(r), m.width))
			} else {
				r = ">" + strings.TrimPrefix(r, " ")
			}
		}
		out = append(out, t.Trunc(r, m.width))
	}
	pos := fmt.Sprintf("  %d–%d of %d", off+1, min(len(rows), off+h), len(rows))
	out = append(out, t.Dim(pos+m.rowHint(gs)))
	return strings.Join(out, "\n")
}

// rowHint explains the selected row (what Enter will do, or why it is partial).
func (m *model_) rowHint(gs []*agg.Group) string {
	if m.cursor[m.tab] >= len(gs) {
		return ""
	}
	g := gs[m.cursor[m.tab]]
	if !g.Complete() {
		var ms []string
		for k := range g.Unpriced {
			ms = append(ms, k)
		}
		return "  · unpriced: " + strings.Join(ms, ", ")
	}
	if m.tab == 5 {
		return "  · " + g.Last.In(m.cfg.Loc).Format("2006-01-02 15:04")
	}
	return "  · Enter to drill in"
}

func (m *model_) footer() string {
	t := m.cfg.Theme
	if m.search {
		return t.Trunc("search model/project/session: "+m.input+"█   (Enter apply · Esc cancel)", m.width)
	}
	keys := "tab/1-6 view · ↑↓ move · Enter drill · Esc back · d range · p provider · / search · s sort · r rescan · ? help · q quit"
	short := "1-6 · ↑↓ · Enter · Esc · d p / s · ? · q"
	if m.width < 100 {
		keys = short
	}
	st := fmt.Sprintf("%s · sort: %s", m.rangeNm, [...]string{"tokens", "cost"}[m.sort])
	if m.cancelled {
		st = t.Warn("partial (scan cancelled) · ") + st
	}
	return t.Trunc(t.Dim(keys), m.width) + "\n" + t.Trunc(t.Dim(st), m.width)
}

func (m *model_) helpView() string {
	t := m.cfg.Theme
	lines := []string{
		t.Head("KEYS"),
		"  tab / shift+tab, ← →, 1-6   switch view (Esc or o: back to Overview)",
		"  ↑ ↓ j k, PgUp PgDn, g G     move / scroll",
		"  Enter                       drill into the selected day, provider, model or project",
		"  Esc / Backspace             undo the last drill-down, then clear search, then Overview",
		"  d                           range: 7d → 30d → 90d → all time",
		"  p                           cycle provider filter",
		"  /                           search model, project or session text",
		"  s                           sort by tokens or by estimated cost",
		"  c                           clear every filter",
		"  r                           rescan files (cached files are not re-read)",
		"  q / Ctrl-C                  quit",
		"",
		t.Dim("Any key closes this help."),
	}
	for i := range lines {
		lines[i] = t.Trunc(lines[i], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m *model_) loadingView() string {
	t := m.cfg.Theme
	p := m.prog
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	bw := max(10, min(40, m.width-20))
	var b strings.Builder
	b.WriteString(t.Bold("gauge-and-grain") + "\n\n")
	b.WriteString(fmt.Sprintf("Reading local usage files… %d/%d\n", p.Done, p.Total))
	b.WriteString(t.Bar(frac, bw) + fmt.Sprintf(" %3.0f%%\n\n", frac*100))
	b.WriteString(t.Dim("First scans of a large history take a while; later runs only read what changed.\n"))
	b.WriteString(t.Dim("Esc cancels cleanly (finished files stay cached) · Ctrl-C quits\n"))
	var out []string
	for _, l := range strings.Split(b.String(), "\n") {
		out = append(out, t.Trunc(l, m.width))
	}
	return strings.Join(out, "\n")
}

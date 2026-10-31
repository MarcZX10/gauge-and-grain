package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/render"
)

// compareRowLimit caps the provider and model rows; the rest are counted.
const compareRowLimit = 25

const compareUsage = `gg compare PERIOD_A PERIOD_B [filters]

  Compares two periods side by side. A period is one of:
    14d                      the last 14 days
    2026-10-29               that day
    2026-10                  that month
    2026                     that year
    2026-10-01..2026-10-15   an inclusive range
    all                      everything
  Filters (--provider, --model, --project) and --prices, --no-cache, --no-color,
  --ascii apply to both periods. Changes are B against A.
`

// compareSide is one period of a comparison: the argument as typed and its summary.
type compareSide struct {
	label string
	sum   *agg.Summary
}

// parsePeriod sets the period flags of o from one period argument, so the
// same filterFrom that reads --days, --from and --to validates it.
func parsePeriod(arg string, o *opts) error {
	s := strings.TrimSpace(arg)
	bad := fmt.Errorf("bad period %q (see `gg help`)", arg)
	switch {
	case strings.EqualFold(s, "all"):
		o.all = true
	case strings.HasSuffix(s, "d") && allDigits(s[:len(s)-1]):
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n < 1 {
			return bad
		}
		o.days = n
	case strings.Contains(s, ".."):
		a, b, _ := strings.Cut(s, "..")
		if a == "" || b == "" {
			return bad
		}
		o.from, o.to = a, b
	case len(s) == 4 && allDigits(s):
		o.from, o.to = s+"-01-01", s+"-12-31"
	case len(s) == 7 && s[4] == '-':
		t, err := time.Parse("2006-01", s)
		if err != nil {
			return bad
		}
		o.from = t.Format("2006-01-02")
		o.to = t.AddDate(0, 1, -1).Format("2006-01-02")
	default: // a single day; filterFrom reports a malformed one
		o.from, o.to = s, s
	}
	return nil
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// compareFilters turns the two period arguments into filters that share the
// provider, model and project filters of base.
func compareFilters(base opts, args []string, now time.Time) ([]agg.Filter, error) {
	var out []agg.Filter
	for _, arg := range args {
		o := base
		if err := parsePeriod(arg, &o); err != nil {
			return nil, err
		}
		f, _, err := filterFrom(&o, now)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// summarizeSides summarises each period over the same events.
func summarizeSides(events []model.Event, args []string, fs []agg.Filter, pt *pricing.Table, loc *time.Location) []compareSide {
	var sides []compareSide
	for i := range fs {
		sides = append(sides, compareSide{label: args[i], sum: agg.Summarize(events, fs[i], pt, loc)})
	}
	return sides
}

// costKnown is true when both sides are fully priced, so their costs can be compared.
func costKnown(a, b *agg.Group) bool {
	return a.Complete() && b.Complete() && a.CostKnown() && b.CostKnown()
}

// changeText states the change from a to b as an arrow and a percentage of a.
// "new" means a is zero, "—" means both are zero, "n/a" means the values
// cannot be compared (known is false).
func changeText(t render.Theme, a, b float64, known bool) string {
	switch {
	case !known:
		return "n/a"
	case a <= 0 && b <= 0:
		return "—"
	case a <= 0:
		return "new"
	}
	f, _ := agg.Change(b, a)
	return t.Arrow(f)
}

// costText is the estimated cost of a group, flagged when some of it is unpriced.
func costText(t render.Theme, g *agg.Group) string {
	switch {
	case g.Tokens() == 0:
		return "—"
	case g.PricedTok == 0:
		return t.Warn("unpriced")
	case !g.Complete():
		return t.Warn("≥ " + render.Cost(g.Cost))
	default:
		return render.Cost(g.Cost)
	}
}

// shareChange is the change in cache share in percentage points.
func shareChange(a, b *agg.Group) string {
	sa, oka := a.CacheShare()
	sb, okb := b.CacheShare()
	if !oka || !okb {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f pts", (sb-sa)*100)
}

// periodSpan is the first and last day shown for a period. An unbounded side
// uses the first and last day with usage; zero when there is none.
func periodSpan(s compareSide) (time.Time, time.Time) {
	loc := s.sum.Loc
	from, to := s.sum.Filter.From, s.sum.Filter.To
	if from.IsZero() {
		from = s.sum.Total.First
	}
	if to.IsZero() {
		to = s.sum.Total.Last
	} else {
		to = to.AddDate(0, 0, -1) // To is exclusive
	}
	if from.IsZero() || to.IsZero() {
		return time.Time{}, time.Time{}
	}
	return from.In(loc), to.In(loc)
}

func periodLine(t render.Theme, tag string, s compareSide) string {
	from, to := periodSpan(s)
	dates := "no usage"
	if !from.IsZero() {
		start := from.Format("2 Jan")
		if from.Year() != to.Year() {
			start = from.Format("2 Jan 2006")
		}
		dates = start + " – " + to.Format("2 Jan 2006")
	}
	return fmt.Sprintf("  %s  %s  %s", tag, t.Pad(s.label, 14), dates)
}

// groupRow is one provider or model in both periods. A missing side is an
// empty group, which has zero tokens and cost.
type groupRow struct {
	key, name string
	a, b      *agg.Group
}

// groupRows pairs the groups of the two periods by key, busiest in B first.
func groupRows(a, b []*agg.Group) []groupRow {
	ma, mb := map[string]*agg.Group{}, map[string]*agg.Group{}
	names := map[string]string{}
	for _, g := range a {
		ma[g.Key], names[g.Key] = g, g.Label
	}
	for _, g := range b {
		mb[g.Key], names[g.Key] = g, g.Label
	}
	var rows []groupRow
	for k := range names {
		r := groupRow{key: k, name: names[k], a: ma[k], b: mb[k]}
		if r.a == nil {
			r.a = &agg.Group{}
		}
		if r.b == nil {
			r.b = &agg.Group{}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if x, y := rows[i].b.Tokens(), rows[j].b.Tokens(); x != y {
			return x > y
		}
		return rows[i].key < rows[j].key
	})
	return rows
}

// table lays out rows under a dimmed header. The first column is left
// aligned and the others are right aligned.
func table(t render.Theme, head []string, rows [][]string) string {
	w := make([]int, len(head))
	for i, h := range head {
		w[i] = render.Width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			w[i] = max(w[i], render.Width(c))
		}
	}
	line := func(cells []string, style func(string) string) string {
		var parts []string
		for i, c := range cells {
			if i == 0 {
				parts = append(parts, t.Pad(c, w[i]))
			} else {
				parts = append(parts, t.PadL(c, w[i]))
			}
		}
		return "  " + style(strings.Join(parts, "  ")) + "\n"
	}
	var b strings.Builder
	b.WriteString(line(head, t.Dim))
	for _, r := range rows {
		b.WriteString(line(r, func(s string) string { return s }))
	}
	return b.String()
}

func totalsRows(t render.Theme, a, b *agg.Group) [][]string {
	sa, oka := a.CacheShare()
	sb, okb := b.CacheShare()
	share := func(s float64, ok bool) string {
		if !ok {
			return "n/a"
		}
		return render.Pct(s)
	}
	return [][]string{
		{"Tokens", render.Tokens(a.Tokens()), render.Tokens(b.Tokens()),
			changeText(t, float64(a.Tokens()), float64(b.Tokens()), true)},
		{"Est. cost", costText(t, a), costText(t, b),
			changeText(t, a.Cost, b.Cost, costKnown(a, b))},
		{"Sessions", render.Num(len(a.Sessions)), render.Num(len(b.Sessions)),
			changeText(t, float64(len(a.Sessions)), float64(len(b.Sessions)), true)},
		{"API calls", render.Num(a.Events), render.Num(b.Events),
			changeText(t, float64(a.Events), float64(b.Events), true)},
		{"Cache share", share(sa, oka), share(sb, okb), shareChange(a, b)},
	}
}

func groupTable(t render.Theme, title string, rows []groupRow) string {
	var cells [][]string
	for i, r := range rows {
		if i == compareRowLimit {
			cells = append(cells, []string{fmt.Sprintf("… %d more", len(rows)-compareRowLimit)})
			break
		}
		cells = append(cells, []string{r.name,
			render.Tokens(r.a.Tokens()), render.Tokens(r.b.Tokens()),
			changeText(t, float64(r.a.Tokens()), float64(r.b.Tokens()), true),
			costText(t, r.a), costText(t, r.b),
			changeText(t, r.a.Cost, r.b.Cost, costKnown(r.a, r.b))})
	}
	head := []string{title, "A tokens", "B tokens", "Change", "A cost", "B cost", "Change"}
	return table(t, head, cells)
}

// compareReport renders the two periods side by side. It is what gg compare prints.
func compareReport(t render.Theme, sides []compareSide, filters string) string {
	a, b := sides[0], sides[1]
	var s strings.Builder
	s.WriteString(t.Section("Compare", "change is B against A: (B - A) / A") + "\n")
	s.WriteString(periodLine(t, "A", a) + "\n")
	s.WriteString(periodLine(t, "B", b) + "\n")
	if filters != "" {
		s.WriteString("  " + t.Dim("filters: "+filters) + "\n")
	}
	s.WriteString("\n" + t.Section("Totals", "") + "\n")
	s.WriteString(table(t, []string{"Metric", a.label, b.label, "Change (vs A)"},
		totalsRows(t, a.sum.Total, b.sum.Total)))
	s.WriteString("\n" + t.Section("Providers", "sorted by B") + "\n")
	s.WriteString(groupTable(t, "Provider", groupRows(a.sum.ByProvider, b.sum.ByProvider)))
	s.WriteString("\n" + t.Section("Models", "sorted by B") + "\n")
	s.WriteString(groupTable(t, "Model", groupRows(a.sum.ByModel, b.sum.ByModel)))
	s.WriteString("\n" + t.Dim("  new: nothing in A to compare with.  —: nothing in either period.") + "\n")
	s.WriteString(t.Dim("  n/a: the cost change needs both periods fully priced (see gg notes).") + "\n")
	s.WriteString(t.Dim("  Cache share changes are in percentage points.") + "\n")
	return s.String()
}

// compareCmd is `gg compare A B`. Its periods are positional, so it parses
// its own arguments instead of the report flags.
func compareCmd(args []string) error {
	var o opts
	fs := flag.NewFlagSet("gg compare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	o.bind(fs)
	var periods []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				sayf(compareUsage)
				return nil
			}
			return fmt.Errorf("%v (see `gg help`)", err)
		}
		if fs.NArg() == 0 {
			break
		}
		periods = append(periods, fs.Arg(0))
		args = fs.Args()[1:]
	}
	var bad error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "days", "from", "to", "all":
			bad = errors.New("gg compare takes its periods as arguments, not --days, --from, --to or --all")
		case "format", "by", "daily", "notes", "compact", "c", "watch", "unpriced":
			bad = fmt.Errorf("--%s does not apply to gg compare", f.Name)
		}
	})
	if bad != nil {
		return bad
	}
	if len(periods) != 2 {
		return errors.New("usage: gg compare PERIOD_A PERIOD_B (see `gg help`)")
	}
	now := time.Now()
	filters, err := compareFilters(o, periods, now) // validate before the scan
	if err != nil {
		return err
	}
	pt := loadPrices(&o)
	provs, err := selected(&o)
	if err != nil {
		return err
	}
	cd := cacheDir()
	if o.noCache {
		cd = ""
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res := scanWithProgress(ctx, provs, cd, &o)
	for _, e := range res.Errors {
		fmt.Fprintln(os.Stderr, "gg: warning: unreadable:", e)
	}
	sides := summarizeSides(res.Events, periods, filters, pt, now.Location())
	sayn(compareReport(theme(&o, os.Stdout), sides, describeFilters(&o)))
	if res.Cancelled {
		os.Exit(130)
	}
	return nil
}

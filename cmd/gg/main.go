// gg — gauge-and-grain: local-first usage analytics for AI coding assistants.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"

	"github.com/MarcZX10/gauge-and-grain/internal/agg"
	"github.com/MarcZX10/gauge-and-grain/internal/pricing"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
	"github.com/MarcZX10/gauge-and-grain/internal/render"
	"github.com/MarcZX10/gauge-and-grain/internal/scan"
	"github.com/MarcZX10/gauge-and-grain/internal/tui"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

func init() {
	if version != "dev" && !strings.HasPrefix(version, "v") {
		version = "v" + version // GoReleaser strips the leading v
	}
	if version == "dev" { // `go install ...@vX.Y.Z` embeds the module version
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
	}
}

const usage = `gg — where does your AI usage go?  (gauge-and-grain %s)

Usage:
  gg [flags]                 interactive TUI on a terminal, compact report otherwise
  gg report [flags]          compact report
  gg tui [flags]             interactive TUI
  gg export [flags]          JSON or CSV export  (--format json|csv, --by day|provider|model|project|session|events)
  gg sources                 which tools were found and what can be read
  gg prices [--unpriced]     price table, sources and dates
  gg cache clear             delete the incremental cache
  gg version

Filters (report, tui, export):
  --days N        last N days including today (default 30)
  --from DATE     start date YYYY-MM-DD (local time)     --to DATE   end date, inclusive
  --all           whole history
  --provider ID   claude | codex   (repeatable, or comma separated)
  --model TEXT    model name contains TEXT (repeatable)
  --project TEXT  project path contains TEXT (repeatable)

Other:
  --no-color      plain output (also honoured: NO_COLOR, TERM=dumb, non-terminal stdout)
  --ascii         ASCII-only bars and arrows
  --no-cache      do not read or write the incremental cache
  --prices FILE   extra/override prices (default: <config dir>/prices.json)
  --daily         (report) add a per-day table

Everything is read from local files; nothing is sent anywhere.
`

type multi []string

func (m *multi) String() string { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

type opts struct {
	days                    int
	from, to                string
	all                     bool
	prov, models, projects  multi
	noColor, ascii, noCache bool
	pricesFile              string
	daily                   bool
	format, by              string
	unpriced                bool
}

func (o *opts) bind(fs *flag.FlagSet) {
	fs.IntVar(&o.days, "days", 30, "")
	fs.StringVar(&o.from, "from", "", "")
	fs.StringVar(&o.to, "to", "", "")
	fs.BoolVar(&o.all, "all", false, "")
	fs.Var(&o.prov, "provider", "")
	fs.Var(&o.models, "model", "")
	fs.Var(&o.projects, "project", "")
	fs.BoolVar(&o.noColor, "no-color", false, "")
	fs.BoolVar(&o.noColor, "plain", false, "")
	fs.BoolVar(&o.ascii, "ascii", false, "")
	fs.BoolVar(&o.noCache, "no-cache", false, "")
	fs.StringVar(&o.pricesFile, "prices", "", "")
	fs.BoolVar(&o.daily, "daily", false, "")
	fs.StringVar(&o.format, "format", "json", "")
	fs.StringVar(&o.by, "by", "day", "")
	fs.BoolVar(&o.unpriced, "unpriced", false, "")
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, version) }
}

func configDir() string {
	if d := os.Getenv("GG_CONFIG_DIR"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "gauge-and-grain")
}

func cacheDir() string {
	if d := os.Getenv("GG_CACHE_DIR"); d != "" {
		return d
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "gauge-and-grain")
}

func main() {
	enableVT()
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help":
		fmt.Printf(usage, version)
		return
	case "version":
		fmt.Println("gg", version)
		return
	case "cache":
		if len(args) == 1 && args[0] == "clear" {
			n, err := scan.ClearCache(cacheDir())
			if err != nil {
				die(err)
			}
			fmt.Printf("removed %d cache files from %s\n", n, cacheDir())
			return
		}
		die(errors.New("usage: gg cache clear"))
	case "", "report", "tui", "export", "sources", "prices":
	default:
		die(fmt.Errorf("unknown command %q (try `gg help`)", cmd))
	}
	var o opts
	fs := flag.NewFlagSet("gg", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o.bind(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Printf(usage, version)
			return
		}
		die(err)
	}
	if fs.NArg() > 0 {
		die(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if err := run(cmd, &o); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "gg:", err)
	os.Exit(1)
}

func theme(o *opts, f *os.File) render.Theme {
	color := isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
	if os.Getenv("FORCE_COLOR") != "" {
		color = true
	}
	if o.noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		color = false
	}
	return render.Theme{Color: color, ASCII: o.ascii || !utf8Locale()}
}

func termWidth(f *os.File) int {
	if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
		return w
	}
	if c, _ := strconv.Atoi(os.Getenv("COLUMNS")); c > 0 {
		return c
	}
	return 100
}

func selected(o *opts) ([]providers.Provider, error) {
	if len(o.prov) == 0 {
		var out []providers.Provider
		for _, p := range providers.All() {
			if p.ParserVersion() > 0 {
				out = append(out, p)
			}
		}
		return out, nil
	}
	var out []providers.Provider
	for _, id := range o.prov {
		p := providers.ByID(strings.ToLower(id))
		if p == nil {
			return nil, fmt.Errorf("unknown provider %q (known: claude, codex, gemini)", id)
		}
		if p.ParserVersion() == 0 {
			return nil, fmt.Errorf("provider %q is detected but not supported yet; see `gg sources`", id)
		}
		out = append(out, p)
	}
	return out, nil
}

func filterFrom(o *opts, now time.Time) (agg.Filter, string, error) {
	loc := now.Location()
	day := func(s string) (time.Time, error) {
		t, err := time.ParseInLocation("2006-01-02", s, loc)
		if err != nil {
			return t, fmt.Errorf("bad date %q: use YYYY-MM-DD", s)
		}
		return t, nil
	}
	var f agg.Filter
	f.Providers = o.prov
	f.Models, f.Projects = o.models, o.projects
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	name := ""
	switch {
	case o.from != "" || o.to != "":
		name = "custom range"
		if o.from != "" {
			t, err := day(o.from)
			if err != nil {
				return f, "", err
			}
			f.From = t
		}
		if o.to != "" {
			t, err := day(o.to)
			if err != nil {
				return f, "", err
			}
			f.To = t.AddDate(0, 0, 1)
		} else {
			f.To = startToday.AddDate(0, 0, 1)
		}
		if f.From.IsZero() {
			name = "up to " + o.to
		}
		if !f.From.IsZero() && !f.To.After(f.From) {
			return f, "", errors.New("--to must not be before --from")
		}
	case o.all:
		name = "all time"
	default:
		if o.days < 1 {
			return f, "", errors.New("--days must be at least 1")
		}
		f.From = startToday.AddDate(0, 0, -(o.days - 1))
		f.To = startToday.AddDate(0, 0, 1)
		name = render.RangeLabel(o.days)
	}
	return f, name, nil
}

func loadPrices(o *opts) *pricing.Table {
	path := o.pricesFile
	if path == "" && configDir() != "" {
		path = filepath.Join(configDir(), "prices.json")
	}
	pt, err := pricing.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gg: warning:", err)
	}
	if pt == nil {
		die(errors.New("could not load embedded prices"))
	}
	return pt
}

func detectAll() (map[string]providers.Detection, map[string]int) {
	d := map[string]providers.Detection{}
	for _, p := range providers.All() {
		d[p.ID()] = p.Detect()
	}
	return d, nil
}

func run(cmd string, o *opts) error {
	switch cmd {
	case "sources":
		return cmdSources(o)
	case "prices":
		return cmdPrices(o)
	}
	if cmd == "" {
		if isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd()) {
			cmd = "tui"
		} else {
			cmd = "report"
		}
	}
	if cmd == "export" && o.format != "json" && o.format != "csv" {
		return fmt.Errorf("--format must be json or csv")
	}
	now := time.Now()
	f, rangeName, err := filterFrom(o, now)
	if err != nil {
		return err
	}
	provs, err := selected(o)
	if err != nil {
		return err
	}
	pt := loadPrices(o)
	cd := cacheDir()
	if o.noCache {
		cd = ""
	}
	detect, _ := detectAll()

	if cmd == "tui" {
		return tui.Run(tui.Config{
			Providers: provs, Detect: detect, Prices: pt, CacheDir: cd, Filter: f, RangeName: rangeName,
			Theme: theme(o, os.Stdout), Loc: now.Location(), DaysFlag: o.days, All: o.all,
			Bounded: o.from == "" && o.to == "",
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res := scanWithProgress(ctx, provs, cd, o)
	c := &render.Ctx{
		Sum:    agg.Summarize(res.Events, f, pt, now.Location()),
		Detect: detect, Files: res.Files, Stats: res.Stats, Cached: res.Cached, Parsed: res.Parsed,
		Duplicate: res.Duplicate, Errors: len(res.Errors), Cancelled: res.Cancelled, Prices: pt,
		RangeName: rangeName, Filters: describeFilters(o),
	}
	for _, e := range res.Errors {
		fmt.Fprintln(os.Stderr, "gg: warning: unreadable:", e)
	}
	switch cmd {
	case "export":
		var err error
		if o.format == "json" {
			err = render.JSON(os.Stdout, c)
		} else {
			err = render.CSV(os.Stdout, c, o.by, res.Events, &f)
		}
		if err != nil {
			return err
		}
	default:
		t := theme(o, os.Stdout)
		fmt.Print(render.Report(t, c, termWidth(os.Stdout), o.daily))
	}
	if res.Cancelled {
		os.Exit(130)
	}
	return nil
}

func describeFilters(o *opts) string {
	var p []string
	if len(o.prov) > 0 {
		p = append(p, "provider="+strings.Join(o.prov, ","))
	}
	if len(o.models) > 0 {
		p = append(p, "model~"+strings.Join(o.models, ","))
	}
	if len(o.projects) > 0 {
		p = append(p, "project~"+strings.Join(o.projects, ","))
	}
	return strings.Join(p, " ")
}

// scanWithProgress shows a one-line progress meter on stderr when it is a
// terminal, so a slow first scan is never a silent hang.
func scanWithProgress(ctx context.Context, provs []providers.Provider, cacheDir string, o *opts) *scan.Result {
	show := isatty.IsTerminal(os.Stderr.Fd())
	var last time.Time
	progress := func(p scan.Progress) {
		if !show || (time.Since(last) < 80*time.Millisecond && p.Done != p.Total) {
			return
		}
		last = time.Now()
		fmt.Fprintf(os.Stderr, "\r\x1b[2Kscanning %d/%d files · %s read   (Ctrl-C to cancel)", p.Done, p.Total, render.Tokens(p.Bytes/1000)+"B")
	}
	res := scan.Run(ctx, scan.Options{Providers: provs, CacheDir: cacheDir, Progress: progress})
	if show {
		fmt.Fprint(os.Stderr, "\r\x1b[2K")
	}
	return res
}

func cmdSources(o *opts) error {
	t := theme(o, os.Stdout)
	fmt.Println(t.Section("Sources", "what was found on this machine; only local files are read"))
	for _, p := range providers.All() {
		d := p.Detect()
		mark := map[providers.Status]string{providers.Ready: "ok", providers.NoData: "empty", providers.NotFound: "missing", providers.Unsupported: "unsupported"}[d.Status]
		if d.Status == providers.Unsupported || d.Status == providers.NoData {
			mark = t.Warn(mark)
		}
		fmt.Printf("  %s %s %s\n", t.Pad(p.Name(), 14), t.Pad(mark, 12), d.Detail)
		for _, dir := range d.Dirs {
			fmt.Printf("  %s %s\n", t.Pad("", 14), t.Dim(dir))
		}
		if d.Files > 0 {
			fmt.Printf("  %s %s\n", t.Pad("", 14), t.Dim(fmt.Sprintf("%s files", render.Num(d.Files))))
		}
	}
	fmt.Println()
	fmt.Println(t.Dim("  Cache: " + cacheDir() + "   Prices override: " + filepath.Join(configDir(), "prices.json")))
	return nil
}

func cmdPrices(o *opts) error {
	t := theme(o, os.Stdout)
	pt := loadPrices(o)
	fmt.Println(t.Section("Prices", "USD per 1M tokens — list prices, offline, inspectable"))
	for _, s := range pt.Sources {
		fmt.Printf("  %s  %s  %s\n", t.Pad(s.ID, 10), t.Pad("updated "+s.Updated, 18), t.Dim(s.URL))
		if s.Note != "" {
			fmt.Printf("  %s  %s\n", t.Pad("", 10), t.Dim(s.Note))
		}
	}
	fmt.Println()
	fmt.Printf("  %s %s %s %s %s\n", t.Dim(t.Pad("model", 22)), t.Dim(t.PadL("input", 7)), t.Dim(t.PadL("output", 8)), t.Dim(t.PadL("cache rd", 9)), t.Dim("source"))
	for i := range pt.Models {
		m := &pt.Models[i]
		cr := "derived"
		if m.CacheRead != nil {
			cr = fmt.Sprintf("%.3f", *m.CacheRead)
		} else {
			cr = fmt.Sprintf("%.3f*", m.Input*0.1)
		}
		src := m.Source
		if m.User {
			src += " (yours)"
		}
		fmt.Printf("  %s %s %s %s %s\n", t.Pad(m.ID, 22), t.PadL(fmt.Sprintf("%.2f", m.Input), 7), t.PadL(fmt.Sprintf("%.2f", m.Output), 8), t.PadL(cr, 9), src)
	}
	fmt.Println()
	fmt.Println(t.Dim("  * derived as 0.1x input (not listed by the vendor)."))
	fmt.Println(t.Dim("  Add or override models in " + filepath.Join(configDir(), "prices.json") + " (same format; see docs/PRICING.md)."))
	if o.unpriced {
		fmt.Println()
		return unpricedReport(o, pt, t)
	}
	return nil
}

func unpricedReport(o *opts, pt *pricing.Table, t render.Theme) error {
	provs, err := selected(o)
	if err != nil {
		return err
	}
	cd := cacheDir()
	if o.noCache {
		cd = ""
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res := scanWithProgress(ctx, provs, cd, o)
	seen := map[string]int64{}
	for i := range res.Events {
		if pt.Lookup(res.Events[i].Model) == nil {
			seen[res.Events[i].Provider+"/"+res.Events[i].Model] += res.Events[i].Total()
		}
	}
	fmt.Println(t.Section("Unpriced models in your history", "tokens counted, cost excluded"))
	if len(seen) == 0 {
		fmt.Println("  none")
	}
	for m, n := range seen {
		fmt.Printf("  %s %s\n", t.Pad(m, 36), render.Tokens(n))
	}
	return nil
}

// utf8Locale is false only when the locale explicitly names a non-UTF-8
// charset (e.g. LANG=C). Unknown or unset is treated as UTF-8-capable.
func utf8Locale() bool {
	for _, v := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if l := os.Getenv(v); l != "" {
			l = strings.ToLower(l)
			return strings.Contains(l, "utf-8") || strings.Contains(l, "utf8")
		}
	}
	return true
}

// Package pricing maps model ids to API list prices. Prices are embedded for
// offline use, carry their source and date, and can be overridden by the user.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

//go:embed prices.json
var embedded []byte

// Price is USD per 1M tokens.
type Price struct {
	ID           string   `json:"id"`
	Provider     string   `json:"provider,omitempty"` // "anthropic" rows get cache-write defaults
	Source       string   `json:"source"`
	Input        float64  `json:"input"`
	Output       float64  `json:"output"`
	CacheRead    *float64 `json:"cache_read,omitempty"`
	CacheWrite5m *float64 `json:"cache_write_5m,omitempty"`
	CacheWrite1h *float64 `json:"cache_write_1h,omitempty"`
	Note         string   `json:"note,omitempty"`
	Aliases      []string `json:"aliases,omitempty"` // other ids priced at this row, matched exactly
	User         bool     `json:"-"`                 // came from the user's override file
}

// Source documents where a group of prices came from.
type Source struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Updated string `json:"updated"`
	Note    string `json:"note"`
}

// Table is a loaded price list.
type Table struct {
	Currency string   `json:"currency,omitempty"` // only USD is supported; empty means USD
	Sources  []Source `json:"sources"`
	Models   []Price  `json:"models"`
	byID     map[string]*Price
}

// Load reads the embedded table and merges an override file when one is
// named. Override entries replace embedded ones with the same id. A named
// file that cannot be read or does not parse is an error: a price the user
// asked for must never be silently replaced by the default one.
func Load(overridePath string) (*Table, error) {
	t := &Table{}
	if err := json.Unmarshal(embedded, t); err != nil {
		return nil, fmt.Errorf("embedded prices: %w", err)
	}
	if err := t.validate("embedded prices"); err != nil {
		return nil, err
	}
	if overridePath != "" {
		b, err := os.ReadFile(overridePath)
		if err != nil {
			return nil, fmt.Errorf("prices file: %w", err)
		}
		var o Table
		if err := json.Unmarshal(b, &o); err != nil {
			return nil, fmt.Errorf("%s: %w", overridePath, err)
		}
		if err := o.validate(overridePath); err != nil {
			return nil, err
		}
		t.Sources = append(t.Sources, o.Sources...)
		for i := range o.Models {
			o.Models[i].User = true
			if o.Models[i].Source == "" {
				o.Models[i].Source = "user"
			}
		}
		t.Models = append(t.Models, o.Models...)
	}
	t.index()
	return t, nil
}

// supportedCurrencies are the units a price file may declare. Prices are
// converted nowhere, so anything else would be silently wrong.
var supportedCurrencies = map[string]bool{"": true, "USD": true}

// validate rejects prices that cannot be right: a negative, NaN or infinite
// rate, or a currency gg does not convert. origin names the file.
func (t *Table) validate(origin string) error {
	if !supportedCurrencies[t.Currency] {
		return fmt.Errorf("%s: currency %q is not supported (only USD)", origin, t.Currency)
	}
	for i := range t.Models {
		if err := checkPrice(&t.Models[i]); err != nil {
			return fmt.Errorf("%s: %w", origin, err)
		}
	}
	return nil
}

func checkPrice(p *Price) error {
	vals := []struct {
		name string
		v    float64
	}{{"input", p.Input}, {"output", p.Output}}
	for _, o := range []struct {
		name string
		v    *float64
	}{{"cache_read", p.CacheRead}, {"cache_write_5m", p.CacheWrite5m}, {"cache_write_1h", p.CacheWrite1h}} {
		if o.v != nil {
			vals = append(vals, struct {
				name string
				v    float64
			}{o.name, *o.v})
		}
	}
	for _, f := range vals {
		if math.IsNaN(f.v) || math.IsInf(f.v, 0) || f.v < 0 {
			return fmt.Errorf("model %q: %s must be a finite price of 0 or more, got %v", p.ID, f.name, f.v)
		}
	}
	return nil
}

func (t *Table) index() {
	t.byID = map[string]*Price{}
	for i := range t.Models { // later entries (user) win
		p := &t.Models[i]
		t.byID[Normalize(p.ID)] = p
		for _, a := range p.Aliases {
			t.byID[Normalize(a)] = p
		}
	}
}

var (
	dateSuffix = regexp.MustCompile(`[-@]\d{8}$|-\d{4}-\d{2}-\d{2}$`)
	// Claude ids write versions as "4-6"; some clients write "4.6".
	claudeDot = regexp.MustCompile(`(\d)\.(\d)`)
)

// Normalize lowercases a model id and strips provider prefixes, context
// suffixes like "[1m]" and trailing snapshot dates. For Claude ids a dot
// between version digits becomes a hyphen ("claude-sonnet-4.6" is 4-6).
func Normalize(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.TrimPrefix(id, "anthropic.")
	if i := strings.Index(id, "["); i >= 0 {
		id = id[:i]
	}
	if strings.HasPrefix(id, "claude") {
		id = claudeDot.ReplaceAllString(id, "$1-$2")
	}
	return dateSuffix.ReplaceAllString(id, "")
}

// Lookup returns the price for a model, or nil when it is unpriced. The id
// must match a row or one of its aliases exactly after Normalize. Prefixes
// are never used: a variant such as "gpt-5-nano" is not "gpt-5".
func (t *Table) Lookup(id string) *Price {
	return t.byID[Normalize(id)]
}

func (p *Price) cacheRead() float64 {
	if p.CacheRead != nil {
		return *p.CacheRead
	}
	return p.Input * 0.1
}

func (p *Price) cacheWrite5m() float64 {
	if p.CacheWrite5m != nil {
		return *p.CacheWrite5m
	}
	if p.Provider == "anthropic" {
		return p.Input * 1.25
	}
	return 0 // OpenAI does not bill cache writes separately
}

func (p *Price) cacheWrite1h() float64 {
	if p.CacheWrite1h != nil {
		return *p.CacheWrite1h
	}
	if p.Provider == "anthropic" {
		return p.Input * 2
	}
	return 0
}

// Cost returns the API-equivalent USD cost of an event under this price.
func (p *Price) Cost(e *model.Event) float64 {
	w1 := min(e.CacheWrite1h, e.CacheWrite)
	w5 := e.CacheWrite - w1
	return (float64(e.Input)*p.Input +
		float64(e.CacheRead)*p.cacheRead() +
		float64(w5)*p.cacheWrite5m() +
		float64(w1)*p.cacheWrite1h() +
		float64(e.Output)*p.Output) / 1e6
}

// StaleAfterDays is how old the price list may be before gg says so.
const StaleAfterDays = 90

// Stale reports how many whole days have passed since the oldest source
// update date, and whether that is more than StaleAfterDays. The oldest is
// used so that one out-of-date vendor table is enough to warn.
func (t *Table) Stale(now time.Time) (days int, stale bool) {
	var oldest time.Time
	for _, s := range t.Sources {
		d, err := time.Parse("2006-01-02", s.Updated)
		if err != nil {
			continue
		}
		if oldest.IsZero() || d.Before(oldest) {
			oldest = d
		}
	}
	if oldest.IsZero() {
		return 0, false
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	days = int(today.Sub(oldest) / (24 * time.Hour))
	return days, days > StaleAfterDays
}

// StaleNote is the one-line warning shown when prices are old.
func StaleNote(days int) string {
	return fmt.Sprintf("prices last updated %d days ago; check docs/PRICING.md", days)
}

// SourceFor returns the source record for a price.
func (t *Table) SourceFor(p *Price) *Source {
	for i := range t.Sources {
		if t.Sources[i].ID == p.Source {
			return &t.Sources[i]
		}
	}
	return nil
}

// Package pricing maps model ids to API list prices. Prices are embedded for
// offline use, carry their source and date, and can be overridden by the user.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

//go:embed prices.json
var embedded []byte

// Price is USD per 1M tokens.
type Price struct {
	ID           string   `json:"id"`
	Source       string   `json:"source"`
	Input        float64  `json:"input"`
	Output       float64  `json:"output"`
	CacheRead    *float64 `json:"cache_read,omitempty"`
	CacheWrite5m *float64 `json:"cache_write_5m,omitempty"`
	CacheWrite1h *float64 `json:"cache_write_1h,omitempty"`
	Derived      []string `json:"derived,omitempty"`
	Note         string   `json:"note,omitempty"`
	User         bool     `json:"-"` // came from the user's override file
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
	Sources []Source `json:"sources"`
	Models  []Price  `json:"models"`
	byID    map[string]*Price
	ids     []string // sorted longest first, for prefix matching
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
	if overridePath != "" {
		b, err := os.ReadFile(overridePath)
		if err != nil {
			return nil, fmt.Errorf("prices file: %w", err)
		}
		var o Table
		if err := json.Unmarshal(b, &o); err != nil {
			return nil, fmt.Errorf("%s: %w", overridePath, err)
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

func (t *Table) index() {
	t.byID = map[string]*Price{}
	for i := range t.Models { // later entries (user) win
		p := &t.Models[i]
		t.byID[Normalize(p.ID)] = p
	}
	for id := range t.byID {
		t.ids = append(t.ids, id)
	}
	sort.Slice(t.ids, func(i, j int) bool { return len(t.ids[i]) > len(t.ids[j]) })
}

var dateSuffix = regexp.MustCompile(`[-@]\d{8}$|-\d{4}-\d{2}-\d{2}$`)

// Normalize lowercases a model id and strips provider prefixes, context
// suffixes like "[1m]" and trailing snapshot dates.
func Normalize(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.TrimPrefix(id, "anthropic.")
	if i := strings.Index(id, "["); i >= 0 {
		id = id[:i]
	}
	return dateSuffix.ReplaceAllString(id, "")
}

// Lookup returns the price for a model, or nil when it is unpriced.
// A model matches exactly, or by id prefix at a "-" boundary (e.g. a
// "claude-opus-5-5-fast" variant), never by a looser substring.
func (t *Table) Lookup(id string) *Price {
	n := Normalize(id)
	if p, ok := t.byID[n]; ok {
		return p
	}
	for _, k := range t.ids {
		if strings.HasPrefix(n, k+"-") {
			return t.byID[k]
		}
	}
	return nil
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
	if strings.HasPrefix(p.ID, "claude") {
		return p.Input * 1.25
	}
	return 0 // OpenAI does not bill cache writes separately
}

func (p *Price) cacheWrite1h() float64 {
	if p.CacheWrite1h != nil {
		return *p.CacheWrite1h
	}
	if strings.HasPrefix(p.ID, "claude") {
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

// SourceFor returns the source record for a price.
func (t *Table) SourceFor(p *Price) *Source {
	for i := range t.Sources {
		if t.Sources[i].ID == p.Source {
			return &t.Sources[i]
		}
	}
	return nil
}

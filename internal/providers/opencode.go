package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, read-only use

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// OpenCodeFamily reads OpenCode and Kilo CLI (which share one on-disk format):
//
//	<data>/opencode/opencode.db                  SQLite (current)
//	<data>/opencode/storage/message/<session>/<id>.json   legacy, one message per file
//
// with <data> = $XDG_DATA_HOME or ~/.local/share (OpenCode uses this path on
// every OS). Kilo CLI uses "kilo" / kilo.db.
//
// Experimental (see Gemini): the format is taken from third-party readers of
// these files and OpenCode's source; it was checked here only against
// databases built to the same schema, not against a real installation.
//
// Assistant messages carry tokens {input, output, reasoning, cache:{read,write}}
// (also per "step-finish" part in newer versions). Input already excludes
// cache. OpenCode computes output as outputTokens − reasoningTokens and bills
// reasoning at the output rate, so reasoning is ADDED to output here (and
// kept as the Reasoning subset). If a message-level token block is empty,
// its step-finish parts are summed instead. Tool-reported cost is not used.
type OpenCodeFamily struct {
	id, name, dir string // dir under the data dir; also the db prefix
}

var opencodeFamily = []OpenCodeFamily{
	{"opencode", "OpenCode", "opencode"},
	{"kilocli", "Kilo CLI", "kilo"},
}

func (o OpenCodeFamily) ID() string         { return o.id }
func (o OpenCodeFamily) Name() string       { return o.name }
func (o OpenCodeFamily) ParserVersion() int { return 1 }
func (o OpenCodeFamily) Experimental() string {
	return "parser follows OpenCode's database/message format as documented by third-party readers; not verified against a real installation"
}
func (o OpenCodeFamily) Fields() Fields {
	return Fields{CacheRead: true, CacheWrite: true, Reasoning: true}
}

func (o OpenCodeFamily) base() string {
	if d := envDirs(strings.ToUpper(o.dir) + "_HOME"); len(d) > 0 {
		return d[0]
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, o.dir)
	}
	return filepath.Join(home(), ".local", "share", o.dir)
}

func (o OpenCodeFamily) Files() []string {
	var out []string
	base := o.base()
	for _, pat := range []string{o.dir + ".db", o.dir + "-*.db"} {
		m, _ := filepath.Glob(filepath.Join(base, pat))
		out = append(out, m...)
	}
	_ = walkExt(existingDirs([]string{filepath.Join(base, "storage", "message")}), func(p string) bool { return filepath.Ext(p) == ".json" }, &out)
	return out
}

func (o OpenCodeFamily) Detect() Detection {
	d := Detection{Dirs: existingDirs([]string{o.base()})}
	if len(d.Dirs) == 0 {
		d.Detail = "no " + o.base() + " (not installed or never used)"
		return d
	}
	d.Files = len(o.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "no database or message files"
		return d
	}
	d.Status, d.Detail = Ready, "SQLite database / message files (experimental parser)"
	return d
}

// WholeFile: legacy .json messages are one document each.
func (o OpenCodeFamily) WholeFile(path string) bool { return filepath.Ext(path) == ".json" }

// ReadsPath: databases are opened by the provider, not read as bytes.
func (o OpenCodeFamily) ReadsPath(path string) bool { return filepath.Ext(path) == ".db" }

type ocTokens struct {
	Input     *int64 `json:"input"`
	Output    *int64 `json:"output"`
	Reasoning *int64 `json:"reasoning"`
	Cache     *struct {
		Read  *int64 `json:"read"`
		Write *int64 `json:"write"`
	} `json:"cache"`
}

func (t *ocTokens) any() bool {
	if t == nil {
		return false
	}
	c := t.Cache
	return val(t.Input) > 0 || val(t.Output) > 0 || val(t.Reasoning) > 0 || (c != nil && (val(c.Read) > 0 || val(c.Write) > 0))
}

type ocMessage struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Role       string `json:"role"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Model      *struct {
		ModelID string `json:"modelID"`
	} `json:"model"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
	Path *struct {
		CWD  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path"`
	Tokens *ocTokens `json:"tokens"`
}

func (m *ocMessage) model() string {
	if m.ModelID != "" {
		return m.ModelID
	}
	if m.Model != nil && m.Model.ModelID != "" {
		return m.Model.ModelID
	}
	return "unknown"
}

// event builds the normalised event; ok=false when there is nothing to count.
func (o OpenCodeFamily) event(m *ocMessage, tok *ocTokens, project string, st *model.ParseStats) (model.Event, bool) {
	if m.Role != "assistant" || !tok.any() || m.Time.Created <= 0 {
		return model.Event{}, false
	}
	reasoning := clamp(val(tok.Reasoning), st)
	e := model.Event{
		Provider: o.id, Model: m.model(), Session: m.SessionID, Project: project, Time: time.UnixMilli(m.Time.Created).UTC(),
		Key:    o.id + ":" + m.SessionID + ":" + m.ID,
		Input:  clamp(val(tok.Input), st),
		Output: clamp(val(tok.Output), st) + reasoning, Reasoning: reasoning,
	}
	if tok.Reasoning != nil {
		e.Has |= model.HasReasoning
	}
	if c := tok.Cache; c != nil {
		e.CacheRead, e.CacheWrite = clamp(val(c.Read), st), clamp(val(c.Write), st)
		if c.Read != nil {
			e.Has |= model.HasCacheRead
		}
		if c.Write != nil {
			e.Has |= model.HasCacheWrite
		}
	}
	return e, true
}

func (o OpenCodeFamily) NewParser(path string, _ *Resume) Parser {
	return &ocJSONParser{o: o}
}

// ocJSONParser reads one legacy message file.
type ocJSONParser struct {
	o   OpenCodeFamily
	st  model.ParseStats
	evs []model.Event
}

func (p *ocJSONParser) State() []byte                             { return nil }
func (p *ocJSONParser) Result() ([]model.Event, model.ParseStats) { return p.evs, p.st }
func (p *ocJSONParser) Line(doc []byte) {
	p.st.Lines++
	var m ocMessage
	if json.Unmarshal(doc, &m) != nil || m.ID == "" {
		p.st.Malformed++
		return
	}
	proj := ""
	if m.Path != nil {
		proj = firstNonEmpty(m.Path.Root, m.Path.CWD)
	}
	if e, ok := p.o.event(&m, m.Tokens, proj, &p.st); ok {
		p.evs = append(p.evs, e)
	}
}

// ParsePath reads a SQLite database read-only.
func (o OpenCodeFamily) ParsePath(ctx context.Context, path string) ([]model.Event, model.ParseStats, error) {
	var st model.ParseStats
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if filepath.VolumeName(path) != "" { // Windows drive letter
		u.Path = "/" + filepath.ToSlash(path)
	}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, st, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	projects := map[string]string{} // session id -> worktree
	if rows, err := db.QueryContext(ctx, "SELECT s.id, p.worktree FROM session s JOIN project p ON p.id = s.project_id"); err == nil {
		for rows.Next() {
			var sid string
			var wt sql.NullString
			if rows.Scan(&sid, &wt) == nil {
				projects[sid] = wt.String
			}
		}
		rows.Close()
	}
	steps := map[string]*ocTokens{} // message id -> summed step-finish tokens
	if rows, err := db.QueryContext(ctx, "SELECT message_id, data FROM part WHERE json_extract(data, '$.type') = 'step-finish'"); err == nil {
		for rows.Next() {
			var id, data string
			if rows.Scan(&id, &data) != nil {
				continue
			}
			var part struct {
				Tokens *ocTokens `json:"tokens"`
			}
			if json.Unmarshal([]byte(data), &part) != nil || part.Tokens == nil {
				st.Malformed++
				continue
			}
			steps[id] = addOC(steps[id], part.Tokens)
		}
		rows.Close()
	}

	rows, err := db.QueryContext(ctx, "SELECT id, session_id, time_created, data FROM message")
	if err != nil {
		return nil, st, err
	}
	defer rows.Close()
	var evs []model.Event
	for rows.Next() {
		var id, sid, data string
		var created int64
		if rows.Scan(&id, &sid, &created, &data) != nil {
			st.Malformed++
			continue
		}
		st.Lines++
		var m ocMessage
		if json.Unmarshal([]byte(data), &m) != nil {
			st.Malformed++
			continue
		}
		m.ID, m.SessionID = id, sid // not part of the data blob
		if m.Time.Created <= 0 {
			m.Time.Created = created
		}
		tok := m.Tokens
		if !tok.any() && steps[id] != nil { // newer versions account per step
			tok = steps[id]
		}
		proj := projects[sid]
		if m.Path != nil && proj == "" {
			proj = firstNonEmpty(m.Path.Root, m.Path.CWD)
		}
		if e, ok := o.event(&m, tok, proj, &st); ok {
			evs = append(evs, e)
		}
	}
	return evs, st, rows.Err()
}

func addOC(a, b *ocTokens) *ocTokens {
	if a == nil {
		a = &ocTokens{Cache: &struct {
			Read  *int64 `json:"read"`
			Write *int64 `json:"write"`
		}{}}
	}
	add := func(dst **int64, v *int64) {
		if v == nil {
			return
		}
		n := val(*dst) + *v
		*dst = &n
	}
	add(&a.Input, b.Input)
	add(&a.Output, b.Output)
	add(&a.Reasoning, b.Reasoning)
	if b.Cache != nil {
		add(&a.Cache.Read, b.Cache.Read)
		add(&a.Cache.Write, b.Cache.Write)
	}
	return a
}

package providers

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Pi reads Pi Agent sessions: ~/.pi/agent/sessions/<project>/*.jsonl.
//
// Experimental (see Gemini): format taken from a third-party reader.
//
// Entries: {"type":"session", id, cwd, provider, modelId}, {"type":"model_change",
// provider, modelId} and {"type":"message", timestamp, message:{role:"assistant",
// provider, model, usage:{input, output, cacheRead, cacheWrite}}}. Pi's usage
// fields are disjoint (input excludes cache), like Anthropic's. Pi also
// records its own cost; it is not used, so prices stay inspectable here.
type Pi struct{}

func (Pi) ID() string         { return "pi" }
func (Pi) Name() string       { return "Pi Agent" }
func (Pi) ParserVersion() int { return 1 }
func (Pi) Experimental() string {
	return "parser follows Pi Agent's session format as documented by third-party readers; not verified against real logs"
}
func (Pi) Fields() Fields { return Fields{CacheRead: true, CacheWrite: true} }

func (Pi) dirs() []string {
	if d := envDirs("PI_HOME"); len(d) > 0 {
		var out []string
		for _, x := range d {
			out = append(out, filepath.Join(x, "agent", "sessions"))
		}
		return existingDirs(out)
	}
	return existingDirs([]string{filepath.Join(home(), ".pi", "agent", "sessions")})
}

func (p Pi) Files() []string { return walkJSONL(p.dirs()) }

func (p Pi) Detect() Detection {
	d := Detection{Dirs: p.dirs()}
	if len(d.Dirs) == 0 {
		d.Detail = "no ~/.pi/agent/sessions (Pi Agent not installed or never used; set PI_HOME to point elsewhere)"
		return d
	}
	d.Files = len(p.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "no session files"
		return d
	}
	d.Status, d.Detail = Ready, "session entries with message usage (experimental parser)"
	return d
}

type piLine struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	Provider  string `json:"provider"`
	ModelID   string `json:"modelId"`
	Message   *struct {
		Role      string `json:"role"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Timestamp int64  `json:"timestamp"`
		Usage     *struct {
			Input      *int64 `json:"input"`
			Output     *int64 `json:"output"`
			CacheRead  *int64 `json:"cacheRead"`
			CacheWrite *int64 `json:"cacheWrite"`
		} `json:"usage"`
	} `json:"message"`
}

type piState struct{ Session, CWD, Provider, Model string }

type piParser struct {
	st  model.ParseStats
	evs []model.Event
	idx map[string]int
	s   piState
}

func (Pi) NewParser(path string, r *Resume) Parser {
	p := &piParser{idx: map[string]int{}, s: piState{Session: strings.TrimSuffix(filepath.Base(path), ".jsonl")}}
	if r != nil {
		p.evs, p.st = r.Events, r.Stats
		if len(r.State) > 0 {
			var s piState
			if gob.NewDecoder(bytes.NewReader(r.State)).Decode(&s) == nil {
				p.s = s
			}
		}
		for i := range p.evs {
			p.idx[p.evs[i].Key] = i
		}
	}
	return p
}

func (p *piParser) State() []byte {
	var b bytes.Buffer
	_ = gob.NewEncoder(&b).Encode(&p.s)
	return b.Bytes()
}
func (p *piParser) Result() ([]model.Event, model.ParseStats) { return p.evs, p.st }

var piMarks = [][]byte{[]byte(`"session"`), []byte(`"model_change"`), []byte(`"usage"`)}

func (p *piParser) Line(line []byte) {
	hit := false
	for _, m := range piMarks {
		if bytes.Contains(line, m) {
			hit = true
			break
		}
	}
	if !hit {
		return
	}
	st := &p.st
	st.Lines++
	var l piLine
	if json.Unmarshal(line, &l) != nil {
		st.Malformed++
		return
	}
	switch l.Type {
	case "session":
		p.s.Session = firstNonEmpty(l.ID, p.s.Session)
		p.s.CWD = firstNonEmpty(l.CWD, p.s.CWD)
		p.s.Provider, p.s.Model = firstNonEmpty(l.Provider, p.s.Provider), firstNonEmpty(l.ModelID, p.s.Model)
	case "model_change":
		p.s.Provider, p.s.Model = firstNonEmpty(l.Provider, p.s.Provider), firstNonEmpty(l.ModelID, p.s.Model)
	case "message":
		m := l.Message
		if m == nil || m.Role != "assistant" || m.Usage == nil {
			return
		}
		ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil {
			st.Malformed++
			return
		}
		mdl := firstNonEmpty(m.Model, p.s.Model, "unknown")
		if prov := firstNonEmpty(m.Provider, p.s.Provider); prov != "" && mdl != "unknown" {
			mdl = prov + "/" + mdl
		}
		p.s.Provider, p.s.Model = firstNonEmpty(m.Provider, p.s.Provider), firstNonEmpty(m.Model, p.s.Model)
		u := m.Usage
		e := model.Event{Provider: "pi", Model: mdl, Session: p.s.Session, Project: p.s.CWD, Time: ts,
			Input: clamp(val(u.Input), st), Output: clamp(val(u.Output), st),
			CacheRead: clamp(val(u.CacheRead), st), CacheWrite: clamp(val(u.CacheWrite), st)}
		if u.CacheRead != nil {
			e.Has |= model.HasCacheRead
		}
		if u.CacheWrite != nil {
			e.Has |= model.HasCacheWrite
		}
		e.Key = "pi:" + p.s.Session + ":" + l.Timestamp + ":" + itoa(m.Timestamp) + ":" + itoa(e.Output)
		if i, ok := p.idx[e.Key]; ok {
			p.evs[i].Merge(&e)
			return
		}
		p.idx[e.Key] = len(p.evs)
		p.evs = append(p.evs, e)
	}
}

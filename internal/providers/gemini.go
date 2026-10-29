package providers

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Gemini reads Gemini CLI chat recordings:
//
//	~/.gemini/tmp/<project>/chats/<session>.json    one JSON document
//	~/.gemini/tmp/<project>/chats/<session>.jsonl   one message per line
//
// Experimental: written from the format Gemini CLI records (its
// chatRecordingService copies the Gemini API's usageMetadata into each
// "gemini" message as `tokens`), as described by an independent open-source
// reader of these files. No real Gemini CLI log was available to verify it.
//
// Semantics (Google's definitions, not disjoint categories):
//
//	tokens.input    whole prompt, INCLUDING tokens.cached
//	tokens.output   candidates only, EXCLUDING tokens.thoughts
//	tokens.tool     tool-use prompt tokens (billed as input)
//	tokens.total    = input + tool + thoughts + output
//
// Normalised here: Input = (input − cached) + tool, CacheRead = cached,
// Output = output + thoughts (reasoning is part of output), Reasoning =
// thoughts. Gemini CLI records no cache writes.
type Gemini struct{}

func (Gemini) ID() string         { return "gemini" }
func (Gemini) Name() string       { return "Gemini CLI" }
func (Gemini) ParserVersion() int { return 1 }
func (Gemini) Experimental() string {
	return "parser follows Gemini CLI's recording format as documented by third-party readers; not verified against real logs"
}
func (Gemini) Fields() Fields {
	return Fields{CacheRead: true, Reasoning: true, Note: "Gemini CLI records no cache writes"}
}

func (Gemini) dirs() []string {
	if d := envDirs("GEMINI_HOME"); len(d) > 0 {
		var out []string
		for _, x := range d {
			out = append(out, filepath.Join(x, "tmp"))
		}
		return existingDirs(out)
	}
	return existingDirs([]string{filepath.Join(home(), ".gemini", "tmp")})
}

func isChatFile(p string) bool {
	if e := filepath.Ext(p); e != ".json" && e != ".jsonl" {
		return false
	}
	return filepath.Base(filepath.Dir(p)) == "chats"
}

func (g Gemini) Files() []string {
	var out []string
	_ = walkExt(g.dirs(), func(p string) bool { return isChatFile(p) }, &out)
	return out
}

func (g Gemini) Detect() Detection {
	d := Detection{Dirs: g.dirs()}
	if len(d.Dirs) == 0 {
		d.Detail = "no ~/.gemini/tmp (Gemini CLI not installed or never used; set GEMINI_HOME to point elsewhere)"
		return d
	}
	d.Files = len(g.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "no chat recordings under tmp/*/chats"
		return d
	}
	d.Status, d.Detail = Ready, "chat recordings with per-message token usage (experimental parser)"
	return d
}

// WholeFile: .json sessions are one document; .jsonl is line-based.
func (Gemini) WholeFile(path string) bool { return filepath.Ext(path) == ".json" }

type geminiTokens struct {
	Input    *int64 `json:"input"`
	Output   *int64 `json:"output"`
	Cached   *int64 `json:"cached"`
	Thoughts *int64 `json:"thoughts"`
	Tool     *int64 `json:"tool"`
}

type geminiMsg struct {
	ID        string        `json:"id"`
	Type      string        `json:"type"`
	Timestamp string        `json:"timestamp"`
	Model     string        `json:"model"`
	Tokens    *geminiTokens `json:"tokens"`
}

type geminiDoc struct {
	SessionID string      `json:"sessionId"`
	Messages  []geminiMsg `json:"messages"`
	Set       *struct {
		Messages []geminiMsg `json:"messages"`
	} `json:"$set"`
	geminiMsg
}

type geminiParser struct {
	st      model.ParseStats
	evs     []model.Event
	idx     map[string]int
	session string
	project string
	path    string
}

func (Gemini) NewParser(path string, r *Resume) Parser {
	p := &geminiParser{idx: map[string]int{}, path: path,
		session: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), project: projectAfter(path, "tmp")}
	if r != nil {
		p.evs, p.st = r.Events, r.Stats
		for i := range p.evs {
			p.idx[p.evs[i].Key] = i
		}
	}
	return p
}

func (p *geminiParser) State() []byte                             { return nil }
func (p *geminiParser) Result() ([]model.Event, model.ParseStats) { return p.evs, p.st }

var gemTokens = []byte(`"tokens"`)

func (p *geminiParser) Line(line []byte) {
	if !bytes.Contains(line, gemTokens) {
		return
	}
	p.st.Lines++
	var d geminiDoc
	if json.Unmarshal(line, &d) != nil {
		p.st.Malformed++
		return
	}
	if d.SessionID != "" {
		p.session = d.SessionID
	}
	for _, m := range d.Messages {
		p.add(m)
	}
	if d.Set != nil {
		for _, m := range d.Set.Messages {
			p.add(m)
		}
	}
	if d.ID != "" && d.Type != "" { // a bare message line (jsonl)
		p.add(d.geminiMsg)
	}
}

func (p *geminiParser) add(m geminiMsg) {
	if m.Type != "gemini" || m.Tokens == nil {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, m.Timestamp)
	if err != nil || m.ID == "" {
		p.st.Malformed++
		return
	}
	t := m.Tokens
	cached := clamp(val(t.Cached), &p.st)
	input := clamp(val(t.Input), &p.st)
	if cached > input { // malformed: cached cannot exceed the prompt
		p.st.Anomalies++
		cached = input
	}
	thoughts := clamp(val(t.Thoughts), &p.st)
	e := model.Event{
		Provider: "gemini", Model: firstNonEmpty(m.Model, "unknown"), Session: p.session, Project: p.project, Time: ts,
		Key:       "gemini:" + p.path + ":" + m.ID,
		Input:     input - cached + clamp(val(t.Tool), &p.st),
		CacheRead: cached,
		Output:    clamp(val(t.Output), &p.st) + thoughts,
		Reasoning: thoughts,
		Has:       model.HasCacheRead,
	}
	if t.Thoughts != nil {
		e.Has |= model.HasReasoning
	}
	if i, ok := p.idx[e.Key]; ok { // later recordings of the same message replace earlier ones
		p.evs[i] = e
		return
	}
	p.idx[e.Key] = len(p.evs)
	p.evs = append(p.evs, e)
}

// projectAfter returns a short project label from the directory following
// `marker` in path, or "".
func projectAfter(path, marker string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == marker && i+1 < len(parts) {
			id := parts[i+1]
			if len(id) > 12 {
				id = id[:12]
			}
			return "gemini:" + id
		}
	}
	return ""
}

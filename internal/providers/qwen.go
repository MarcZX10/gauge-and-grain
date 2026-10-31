package providers

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Qwen reads Qwen Code sessions: ~/.qwen/projects/<project>/chats/<session>.jsonl,
// one record per line. Assistant records carry Gemini-style `usageMetadata`.
//
// Experimental (see Gemini): the format is taken from a third-party reader
// of these files; no real Qwen Code log was available to verify it.
//
// promptTokenCount includes cachedContentTokenCount; thoughtsTokenCount is
// billed as output. Normalised like Gemini CLI.
type Qwen struct{}

func (Qwen) ID() string         { return "qwen" }
func (Qwen) Name() string       { return "Qwen Code" }
func (Qwen) ParserVersion() int { return 2 } // 2: cache read only when cached is present
func (Qwen) Experimental() string {
	return "parser follows Qwen Code's session format as documented by third-party readers; not verified against real logs"
}
func (Qwen) Fields() Fields {
	return Fields{CacheRead: true, Reasoning: true, Note: "Qwen Code records no cache writes"}
}

func (Qwen) dirs() []string {
	if d := envDirs("QWEN_HOME"); len(d) > 0 {
		var out []string
		for _, x := range d {
			out = append(out, filepath.Join(x, "projects"))
		}
		return existingDirs(out)
	}
	return existingDirs([]string{filepath.Join(home(), ".qwen", "projects")})
}

func (q Qwen) Files() []string {
	var out []string
	_ = walkExt(q.dirs(), func(p string) bool {
		return filepath.Ext(p) == ".jsonl" && filepath.Base(filepath.Dir(p)) == "chats"
	}, &out)
	return out
}

func (q Qwen) Detect() Detection {
	d := Detection{Dirs: q.dirs()}
	if len(d.Dirs) == 0 {
		d.Detail = "no ~/.qwen/projects (Qwen Code not installed or never used; set QWEN_HOME to point elsewhere)"
		return d
	}
	d.Files = len(q.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "no session files under projects/*/chats"
		return d
	}
	d.Status, d.Detail = Ready, "session records with usageMetadata (experimental parser)"
	return d
}

type qwenRecord struct {
	UUID      string `json:"uuid"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	Usage     *struct {
		Prompt     *int64 `json:"promptTokenCount"`
		Candidates *int64 `json:"candidatesTokenCount"`
		Thoughts   *int64 `json:"thoughtsTokenCount"`
		Cached     *int64 `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

type qwenParser struct {
	st      model.ParseStats
	evs     []model.Event
	idx     map[string]int
	session string
	project string
}

func (Qwen) NewParser(path string, r *Resume) Parser {
	p := &qwenParser{idx: map[string]int{}, session: strings.TrimSuffix(filepath.Base(path), ".jsonl"), project: qwenProject(path)}
	if r != nil {
		p.evs, p.st = r.Events, r.Stats
		for i := range p.evs {
			p.idx[p.evs[i].Key] = i
		}
	}
	return p
}

func qwenProject(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "projects" && i+1 < len(parts) {
			return "qwen:" + parts[i+1]
		}
	}
	return ""
}

func (p *qwenParser) State() []byte                             { return nil }
func (p *qwenParser) Result() ([]model.Event, model.ParseStats) { return p.evs, p.st }

var qwUsage = []byte(`"usageMetadata"`)

func (p *qwenParser) Line(line []byte) {
	if !bytes.Contains(line, qwUsage) {
		return
	}
	st := &p.st
	st.Lines++
	var r qwenRecord
	if json.Unmarshal(line, &r) != nil {
		st.Malformed++
		return
	}
	if r.Type != "assistant" || r.Usage == nil {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		st.Malformed++
		return
	}
	u := r.Usage
	prompt, cached := clamp(val(u.Prompt), st), clamp(val(u.Cached), st)
	if cached > prompt {
		st.Anomalies++
		cached = prompt
	}
	thoughts := clamp(val(u.Thoughts), st)
	key := "qwen:" + p.session + ":" + r.UUID
	if r.UUID == "" { // no id: identify by time and size
		key = "qwen:" + p.session + ":" + r.Timestamp + ":" + itoa(val(u.Candidates))
	}
	e := model.Event{Provider: "qwen", Model: firstNonEmpty(r.Model, "unknown"), Session: p.session, Project: p.project, Time: ts,
		Key: key, Input: prompt - cached, CacheRead: cached, Output: clamp(val(u.Candidates), st) + thoughts, Reasoning: thoughts}
	if u.Cached != nil { // no field, no cache read: the share is n/a, not 0%
		e.Has |= model.HasCacheRead
	}
	if u.Thoughts != nil {
		e.Has |= model.HasReasoning
	}
	if i, ok := p.idx[key]; ok {
		p.evs[i] = e
		return
	}
	p.idx[key] = len(p.evs)
	p.evs = append(p.evs, e)
}

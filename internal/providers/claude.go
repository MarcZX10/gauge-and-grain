package providers

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Claude reads Claude Code transcripts (~/.claude/projects/**/*.jsonl).
//
// Assumptions (see docs/PROVIDERS.md):
//   - Only "assistant" records with message.usage are billed calls.
//   - Claude Code writes one record per content block, repeating the same
//     message.id + requestId and the same usage; we count each call once and
//     keep the largest counter seen.
//   - input_tokens excludes cache; cache_creation_input_tokens and
//     cache_read_input_tokens are separate. thinking_tokens is a subset of
//     output_tokens.
//   - Sub-agent and resumed-session files repeat history; the global Key
//     dedupes them.
type Claude struct{}

func (Claude) ID() string         { return "claude" }
func (Claude) Name() string       { return "Claude Code" }
func (Claude) ParserVersion() int { return 2 } // 2: tool uses
func (Claude) Fields() Fields {
	return Fields{CacheRead: true, CacheWrite: true, Reasoning: true,
		Note: "reasoning shown only when the log carries thinking_tokens"}
}

func (Claude) dirs() []string {
	if d := envDirs("CLAUDE_CONFIG_DIR"); len(d) > 0 {
		var out []string
		for _, x := range d {
			out = append(out, filepath.Join(x, "projects"))
		}
		return existingDirs(out)
	}
	h := home()
	return existingDirs([]string{
		filepath.Join(h, ".config", "claude", "projects"),
		filepath.Join(h, ".claude", "projects"),
	})
}

func (c Claude) Files() []string { return walkJSONL(c.dirs()) }

func (c Claude) Detect() Detection {
	d := Detection{Dirs: c.dirs()}
	if len(d.Dirs) == 0 {
		d.Detail = "no ~/.claude/projects or ~/.config/claude/projects (set CLAUDE_CONFIG_DIR to point elsewhere)"
		return d
	}
	d.Files = len(c.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "projects directory is empty"
		return d
	}
	d.Status, d.Detail = Ready, "transcripts with per-message usage"
	return d
}

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	SessionU  string `json:"session_id"`
	CWD       string `json:"cwd"`
	RequestID string `json:"requestId"`
	UUID      string `json:"uuid"`
	Message   *struct {
		ID      string        `json:"id"`
		Model   string        `json:"model"`
		Content []claudeBlock `json:"content"`
		Usage   *struct {
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			CacheRead  *int64 `json:"cache_read_input_tokens"`
			CacheWrite *int64 `json:"cache_creation_input_tokens"`
			Details    *struct {
				Thinking *int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
			Creation *struct {
				OneHour int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// claudeBlock is one content block of a message; only tool uses are read.
type claudeBlock struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Input *struct {
		Skill string `json:"skill"`
	} `json:"input"`
}

// claudeTools names the tools one message uses: each tool_use block, with a
// skill shown as skill:<name> (the Skill tool takes the skill's name).
func claudeTools(content []claudeBlock) string {
	var names []string
	for _, b := range content {
		if b.Type != "tool_use" {
			continue
		}
		n := b.Name
		if n == "" {
			n = "(unnamed)"
		}
		if n == "Skill" && b.Input != nil && b.Input.Skill != "" {
			n = "skill:" + b.Input.Skill
		}
		names = append(names, n)
	}
	return model.ToolList(names)
}

var (
	tokUsage  = []byte(`"usage"`)
	tokAssist = []byte(`"assistant"`)
)

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

type claudeParser struct {
	st          model.ParseStats
	evs         []model.Event
	idx         map[string]int
	fileSession string
}

func (Claude) NewParser(path string, r *Resume) Parser {
	p := &claudeParser{idx: map[string]int{}, fileSession: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	if r != nil {
		p.evs, p.st = r.Events, r.Stats
		for i := range p.evs {
			p.idx[p.evs[i].Key] = i
		}
	}
	return p
}

func (p *claudeParser) State() []byte                             { return nil }
func (p *claudeParser) Result() ([]model.Event, model.ParseStats) { return p.evs, p.st }

func (p *claudeParser) Line(line []byte) {
	st := &p.st
	// Cheap pre-filter: most lines are not usage records.
	if !bytes.Contains(line, tokUsage) || !bytes.Contains(line, tokAssist) {
		return
	}
	st.Lines++
	var l claudeLine
	if json.Unmarshal(line, &l) != nil {
		st.Malformed++
		return
	}
	if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
		return
	}
	m := l.Message
	if m.Model == "" || strings.HasPrefix(m.Model, "<") { // "<synthetic>" etc.
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		st.Malformed++
		return
	}
	u := m.Usage
	e := model.Event{
		Provider:  "claude",
		Model:     m.Model,
		Session:   firstNonEmpty(l.SessionID, l.SessionU, p.fileSession),
		Project:   l.CWD,
		Time:      ts,
		Input:     clamp(val(u.Input), st),
		Output:    clamp(val(u.Output), st),
		CacheRead: clamp(val(u.CacheRead), st),
		Tools:     claudeTools(m.Content),
	}
	e.CacheWrite = clamp(val(u.CacheWrite), st)
	if u.CacheRead != nil {
		e.Has |= model.HasCacheRead
	}
	if u.CacheWrite != nil {
		e.Has |= model.HasCacheWrite
	}
	if u.Creation != nil {
		e.CacheWrite1h = clamp(u.Creation.OneHour, st)
	}
	if u.Details != nil && u.Details.Thinking != nil {
		e.Reasoning = clamp(*u.Details.Thinking, st)
		e.Has |= model.HasReasoning
	}
	switch {
	case m.ID != "" && l.RequestID != "":
		e.Key = "claude:" + m.ID + ":" + l.RequestID
	case m.ID != "":
		e.Key = "claude:" + m.ID
	case l.UUID != "":
		e.Key = "claude:uuid:" + l.UUID
	default:
		st.Malformed++ // no identity: cannot be deduplicated safely
		return
	}
	if i, ok := p.idx[e.Key]; ok {
		p.evs[i].Merge(&e)
		return
	}
	p.idx[e.Key] = len(p.evs)
	p.evs = append(p.evs, e)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

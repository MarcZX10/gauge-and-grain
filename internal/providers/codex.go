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

// Codex reads OpenAI Codex rollout files (~/.codex/sessions and
// ~/.codex/archived_sessions).
//
// Assumptions (see docs/PROVIDERS.md):
//   - Newer logs carry one token_usage_record per response with the
//     per-response `usage`; these are keyed by response_id and are preferred.
//   - Older logs only have event_msg/token_count with cumulative
//     total_token_usage, re-broadcast at turn end. We use only increases in
//     the cumulative total (a repeat of the same total adds nothing).
//   - OpenAI reports input_tokens INCLUDING cached tokens; we subtract
//     cached_input_tokens (and cache_write_input_tokens, assumed to be a
//     subset as well) so Input means "uncached input" everywhere.
//   - reasoning_output_tokens is a subset of output_tokens.
//   - The model comes from the latest turn_context before the event.
type Codex struct{}

func (Codex) ID() string         { return "codex" }
func (Codex) Name() string       { return "OpenAI Codex" }
func (Codex) ParserVersion() int { return 1 }
func (Codex) Fields() Fields {
	return Fields{CacheRead: true, CacheWrite: true, Reasoning: true,
		Note: "cache writes are not billed separately by OpenAI"}
}

func (Codex) dirs() []string {
	base := envDirs("CODEX_HOME")
	if len(base) == 0 {
		base = []string{filepath.Join(home(), ".codex")}
	}
	var out []string
	for _, b := range base {
		out = append(out, filepath.Join(b, "sessions"), filepath.Join(b, "archived_sessions"))
	}
	return existingDirs(out)
}

func (c Codex) Files() []string { return walkJSONL(c.dirs()) }

func (c Codex) Detect() Detection {
	d := Detection{Dirs: c.dirs()}
	if len(d.Dirs) == 0 {
		d.Detail = "no ~/.codex/sessions or archived_sessions (set CODEX_HOME to point elsewhere)"
		return d
	}
	d.Files = len(c.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "no rollout files"
		return d
	}
	d.Status, d.Detail = Ready, "rollout files; archived sessions de-duplicated by response id"
	return d
}

type codexUsage struct {
	Input      *int64 `json:"input_tokens"`
	Cached     *int64 `json:"cached_input_tokens"`
	CacheWrite *int64 `json:"cache_write_input_tokens"`
	Output     *int64 `json:"output_tokens"`
	Reasoning  *int64 `json:"reasoning_output_tokens"`
	Total      *int64 `json:"total_tokens"`
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string      `json:"type"`
		ID         string      `json:"id"`
		SessionID  string      `json:"session_id"`
		ThreadID   string      `json:"thread_id"`
		ResponseID string      `json:"response_id"`
		CWD        string      `json:"cwd"`
		Model      string      `json:"model"`
		Usage      *codexUsage `json:"usage"`
		Info       *struct {
			Total *codexUsage `json:"total_token_usage"`
			Last  *codexUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

var (
	cxRecord = []byte(`token_usage_record`)
	cxCount  = []byte(`token_count`)
	cxMeta   = []byte(`session_meta`)
	cxTurn   = []byte(`turn_context`)
)

func (u *codexUsage) event(base model.Event, st *model.ParseStats) (model.Event, bool) {
	if u == nil || (u.Input == nil && u.Output == nil) {
		return base, false
	}
	in := clamp(val(u.Input), st)
	cr := clamp(val(u.Cached), st)
	cw := clamp(val(u.CacheWrite), st)
	e := base
	e.Output = clamp(val(u.Output), st)
	e.CacheRead = min(cr, in)
	e.CacheWrite = min(cw, in-e.CacheRead)
	e.Input = in - e.CacheRead - e.CacheWrite
	if u.Cached != nil {
		e.Has |= model.HasCacheRead
	}
	if u.CacheWrite != nil {
		e.Has |= model.HasCacheWrite
	}
	if u.Reasoning != nil {
		e.Reasoning = min(clamp(*u.Reasoning, st), e.Output)
		e.Has |= model.HasReasoning
	}
	return e, true
}

type codexState struct {
	Session, CWD, CurModel string
	PrevTotal              codexUsage
	HavePrev               bool
	UsingRecords           bool
	Counts                 []model.Event // legacy-path events, kept only when records took over
}

type codexParser struct {
	st              model.ParseStats
	s               codexState
	records, counts []model.Event
	seenResp        map[string]bool
}

func (Codex) NewParser(path string, r *Resume) Parser {
	p := &codexParser{seenResp: map[string]bool{}}
	p.s.Session = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if r != nil {
		p.st = r.Stats
		if len(r.State) > 0 {
			var s codexState
			if gob.NewDecoder(bytes.NewReader(r.State)).Decode(&s) == nil {
				p.s = s
			}
		}
		if p.s.UsingRecords {
			p.records, p.counts = r.Events, p.s.Counts
		} else {
			p.counts = r.Events
		}
		for i := range p.records {
			p.seenResp[strings.TrimPrefix(p.records[i].Key, "codex:resp:")] = true
		}
	}
	return p
}

func (p *codexParser) State() []byte {
	s := p.s
	s.UsingRecords = len(p.records) > 0
	s.Counts = nil
	if s.UsingRecords {
		s.Counts = p.counts
	}
	var b bytes.Buffer
	_ = gob.NewEncoder(&b).Encode(&s)
	return b.Bytes()
}

// Result prefers exact per-response records; cumulative deltas are the
// fallback for older logs.
func (p *codexParser) Result() ([]model.Event, model.ParseStats) {
	if len(p.records) > 0 {
		return p.records, p.st
	}
	return p.counts, p.st
}

func (p *codexParser) Line(line []byte) {
	st := &p.st
	isRec := bytes.Contains(line, cxRecord)
	isCount := bytes.Contains(line, cxCount)
	isMeta := bytes.Contains(line, cxMeta)
	isTurn := bytes.Contains(line, cxTurn)
	if !isRec && !isCount && !isMeta && !isTurn {
		return
	}
	st.Lines++
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		st.Malformed++
		return
	}
	pl := &l.Payload
	switch l.Type {
	case "session_meta":
		p.s.Session = firstNonEmpty(pl.SessionID, pl.ID, p.s.Session)
		if pl.CWD != "" {
			p.s.CWD = pl.CWD
		}
		return
	case "turn_context":
		if pl.Model != "" {
			p.s.CurModel = pl.Model
		}
		if pl.CWD != "" {
			p.s.CWD = pl.CWD
		}
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		st.Malformed++
		return
	}
	base := model.Event{Provider: "codex", Model: firstNonEmpty(p.s.CurModel, "unknown"),
		Session: firstNonEmpty(pl.SessionID, p.s.Session), Project: p.s.CWD, Time: ts}
	switch {
	case l.Type == "token_usage_record":
		if pl.ResponseID == "" {
			st.Malformed++
			return
		}
		if p.seenResp[pl.ResponseID] {
			return
		}
		p.seenResp[pl.ResponseID] = true
		base.Key = "codex:resp:" + pl.ResponseID
		if e, ok := pl.Usage.event(base, st); ok {
			p.records = append(p.records, e)
		} else {
			st.Malformed++
		}
	case l.Type == "event_msg" && pl.Type == "token_count" && pl.Info != nil && pl.Info.Total != nil:
		t := pl.Info.Total
		cur := cumTotal(t)
		prev := cumTotal(&p.s.PrevTotal)
		if p.s.HavePrev && cur == prev {
			return // turn-end re-broadcast of the same cumulative total
		}
		var d *codexUsage
		switch {
		case p.s.HavePrev && cur > prev:
			pt := &p.s.PrevTotal
			d = &codexUsage{
				Input: sub(t.Input, pt.Input), Cached: sub(t.Cached, pt.Cached), CacheWrite: sub(t.CacheWrite, pt.CacheWrite),
				Output: sub(t.Output, pt.Output), Reasoning: sub(t.Reasoning, pt.Reasoning),
			}
		default: // first event, or the counter went backwards (reset)
			d = t
			if p.s.HavePrev && pl.Info.Last != nil {
				d = pl.Info.Last
			}
		}
		p.s.PrevTotal, p.s.HavePrev = *t, true
		base.Key = "codex:cum:" + p.s.Session + ":" + itoa(cur)
		if e, ok := d.event(base, st); ok {
			p.counts = append(p.counts, e)
		}
	}
}

func cumTotal(u *codexUsage) int64 {
	if v := val(u.Total); v != 0 {
		return v
	}
	return val(u.Input) + val(u.Output)
}

func sub(a, b *int64) *int64 {
	v := val(a) - val(b)
	if v < 0 {
		v = 0
	}
	return &v
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

package providers

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// VSCodeExt reads the Cline family of VS Code extensions — Cline, Roo Code,
// Kilo Code, Zoo Code — which all store one directory per task under the
// editor's globalStorage:
//
//	<editor user dir>/globalStorage/<extension id>/tasks/<task>/ui_messages.json
//
// Experimental (see Gemini): format taken from a third-party reader.
//
// ui_messages.json is a JSON array. Each model call is a message
// {"type":"say","say":"api_req_started","ts":<ms>,"text":"<json>"} whose
// text holds {tokensIn, tokensOut, cacheWrites, cacheReads, cost,
// cancelReason}. tokensIn is uncached input; cache counts are separate
// (Anthropic-style). Requests still in flight (no token fields), cancelled
// ones and ones flagged usage_missing are skipped. The extension's own
// `cost` is not used. The model is read from the task: Cline records it in
// task_metadata.json (model_usage, by time); Roo, Kilo and Zoo write
// <model>…</model> into api_conversation_history.json (the last one is
// used for the whole task, so a mid-task model switch is attributed to the
// last model). These extensions do not record a working directory.
type VSCodeExt struct {
	id, name, extID string
	modelFromMeta   bool // Cline: task_metadata.json; others: api_conversation_history.json
}

var vscodeExts = []VSCodeExt{
	{"cline", "Cline", "saoudrizwan.claude-dev", true},
	{"roo", "Roo Code", "rooveterinaryinc.roo-cline", false},
	{"kilo", "Kilo Code", "kilocode.kilo-code", false},
	{"zoo", "Zoo Code", "zoocodeorganization.zoo-code", false},
}

func (v VSCodeExt) ID() string         { return v.id }
func (v VSCodeExt) Name() string       { return v.name }
func (v VSCodeExt) ParserVersion() int { return 1 }
func (v VSCodeExt) Experimental() string {
	return "parser follows the extension's ui_messages.json format as documented by third-party readers; not verified against real logs"
}
func (v VSCodeExt) Fields() Fields {
	return Fields{CacheRead: true, CacheWrite: true, Note: "no working directory is recorded, so projects show as (unknown)"}
}

// vscodeUserDirs lists editor "User" directories across editors and OSes.
// GG_VSCODE_USER_DIRS (a path list) replaces the search, for tests.
func vscodeUserDirs() []string {
	if d := envDirs("GG_VSCODE_USER_DIRS"); len(d) > 0 {
		return d
	}
	gui := []string{"Code", "Code - Insiders", "Cursor", "Windsurf", "VSCodium", "Positron", "Antigravity"}
	var out []string
	h := home()
	for _, f := range gui {
		out = append(out, filepath.Join(h, ".config", f, "User"), filepath.Join(h, "Library", "Application Support", f, "User"))
		if ad := os.Getenv("APPDATA"); ad != "" {
			out = append(out, filepath.Join(ad, f, "User"))
		}
	}
	for _, f := range []string{"vscode-server", "vscode-server-insiders"} {
		out = append(out, filepath.Join(h, "."+f, "data", "User"))
	}
	return existingDirs(out)
}

func (v VSCodeExt) dirs() []string {
	var out []string
	for _, u := range vscodeUserDirs() {
		out = append(out, filepath.Join(u, "globalStorage", v.extID, "tasks"))
	}
	return existingDirs(out)
}

func (v VSCodeExt) Files() []string {
	var out []string
	_ = walkExt(v.dirs(), func(p string) bool { return filepath.Base(p) == "ui_messages.json" }, &out)
	return out
}

func (v VSCodeExt) Detect() Detection {
	d := Detection{Dirs: v.dirs()}
	if len(d.Dirs) == 0 {
		d.Detail = "extension " + v.extID + " not found in any VS Code–style editor"
		return d
	}
	d.Files = len(v.Files())
	if d.Files == 0 {
		d.Status, d.Detail = NoData, "no tasks with ui_messages.json"
		return d
	}
	d.Status, d.Detail = Ready, "task transcripts with per-request usage (experimental parser)"
	return d
}

func (VSCodeExt) WholeFile(string) bool { return true }

type uiMessage struct {
	Type string `json:"type"`
	Say  string `json:"say"`
	TS   int64  `json:"ts"`
	Text string `json:"text"`
}

type apiReq struct {
	TokensIn     *int64  `json:"tokensIn"`
	TokensOut    *int64  `json:"tokensOut"`
	CacheWrites  *int64  `json:"cacheWrites"`
	CacheReads   *int64  `json:"cacheReads"`
	CancelReason *string `json:"cancelReason"`
	UsageMissing bool    `json:"usage_missing"`
}

type extParser struct {
	v    VSCodeExt
	path string
	st   model.ParseStats
	evs  []model.Event
}

func (v VSCodeExt) NewParser(path string, _ *Resume) Parser { return &extParser{v: v, path: path} }

func (p *extParser) State() []byte                             { return nil }
func (p *extParser) Result() ([]model.Event, model.ParseStats) { return p.evs, p.st }

func (p *extParser) Line(doc []byte) {
	st := &p.st
	st.Lines++
	var msgs []uiMessage
	if json.Unmarshal(doc, &msgs) != nil {
		st.Malformed++
		return
	}
	dir := filepath.Dir(p.path)
	task := filepath.Base(dir)
	models := p.taskModels(dir)
	for i, m := range msgs {
		if m.Type != "say" || m.Say != "api_req_started" || m.Text == "" {
			continue
		}
		var r apiReq
		if json.Unmarshal([]byte(m.Text), &r) != nil {
			st.Malformed++
			continue
		}
		if r.CancelReason != nil || r.UsageMissing || (r.TokensIn == nil && r.TokensOut == nil) {
			continue // cancelled, no usage reported, or still in flight
		}
		if m.TS <= 0 {
			st.Malformed++
			continue
		}
		e := model.Event{
			Provider: p.v.id, Model: models.at(m.TS), Session: task, Time: time.UnixMilli(m.TS).UTC(),
			Key:   p.v.id + ":" + task + ":" + itoa(m.TS) + "#" + itoa(int64(i)),
			Input: clamp(val(r.TokensIn), st), Output: clamp(val(r.TokensOut), st),
			CacheRead: clamp(val(r.CacheReads), st), CacheWrite: clamp(val(r.CacheWrites), st),
		}
		if r.CacheReads != nil {
			e.Has |= model.HasCacheRead
		}
		if r.CacheWrites != nil {
			e.Has |= model.HasCacheWrite
		}
		p.evs = append(p.evs, e)
	}
}

// taskModels resolves which model served a request.
type taskModels struct {
	byTime []struct {
		ts int64
		id string
	}
	last string
}

func (t taskModels) at(ts int64) string {
	m := ""
	for _, e := range t.byTime {
		if e.ts <= ts {
			m = e.id
		}
	}
	return firstNonEmpty(m, t.last, "unknown")
}

func (p *extParser) taskModels(dir string) taskModels {
	var tm taskModels
	if p.v.modelFromMeta {
		b, err := os.ReadFile(filepath.Join(dir, "task_metadata.json"))
		if err != nil {
			return tm
		}
		var meta struct {
			ModelUsage []struct {
				TS      int64  `json:"ts"`
				ModelID string `json:"model_id"`
			} `json:"model_usage"`
		}
		if json.Unmarshal(b, &meta) == nil {
			for _, u := range meta.ModelUsage {
				tm.byTime = append(tm.byTime, struct {
					ts int64
					id string
				}{u.TS, u.ModelID})
			}
		}
		return tm
	}
	hist := filepath.Join(dir, "api_conversation_history.json")
	if fi, err := os.Stat(hist); err != nil || fi.Size() > 64<<20 { // huge histories: leave the model unknown
		return tm
	}
	b, err := os.ReadFile(hist)
	if err != nil {
		return tm
	}
	for _, chunk := range bytes.Split(b, []byte("<model>"))[1:] {
		if i := bytes.Index(chunk, []byte("</model>")); i > 0 && i < 200 {
			tm.last = strings.TrimSpace(string(chunk[:i]))
		}
	}
	return tm
}

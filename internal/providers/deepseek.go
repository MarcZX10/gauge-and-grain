package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// DeepSeek reads DeepSeek Harness sessions:
//
//	$DSH_HOME or ~/.dsh / sessions/<workspace>/<session>/session.jsonl.zstd (or session.v4.jsonl.zstd)
//
// a zstd-compressed JSONL event log.
//
// Experimental (see Gemini): format taken from a third-party reader.
//
// Events: {"type":"session", id, cwd, parentSession, seedLength}, and
// {"type":"assistant/message", time(ms), data:{message:{id, source:{model}},
// usage:{inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens,
// reasoningTokens}}}. Input excludes cache (the three are summed to get the
// context size). A session forked from another is "seeded" with the parent's
// messages, which keep their ids: the global de-duplication counts each
// message once. reasoningTokens is assumed to be a subset of outputTokens, as
// in DeepSeek's API; this has not been verified for the harness.
type DeepSeek struct{}

func (DeepSeek) ID() string         { return "deepseek" }
func (DeepSeek) Name() string       { return "DeepSeek Harness" }
func (DeepSeek) ParserVersion() int { return 2 } // 2: output clamped once
func (DeepSeek) Experimental() string {
	return "parser follows DeepSeek Harness's session format as documented by third-party readers; not verified against real logs"
}
func (DeepSeek) Fields() Fields { return Fields{CacheRead: true, CacheWrite: true, Reasoning: true} }

func (DeepSeek) dir() string {
	if d := envDirs("DSH_HOME"); len(d) > 0 {
		x := d[0]
		if x == "~" {
			x = home()
		} else if strings.HasPrefix(x, "~/") || strings.HasPrefix(x, `~\`) {
			x = filepath.Join(home(), x[2:])
		}
		return filepath.Join(x, "sessions")
	}
	return filepath.Join(home(), ".dsh", "sessions")
}

func (d DeepSeek) Files() []string {
	var out []string
	for _, ws := range subdirs(d.dir()) {
		for _, sess := range subdirs(ws) {
			for _, name := range []string{"session.jsonl.zstd", "session.v4.jsonl.zstd"} {
				out = append(out, listFiles(sess, name)...)
			}
		}
	}
	return out
}

func (d DeepSeek) Detect() Detection {
	det := Detection{Dirs: existingDirs([]string{d.dir()})}
	if len(det.Dirs) == 0 {
		det.Detail = "no ~/.dsh/sessions (DeepSeek Harness not installed or never used; set DSH_HOME to point elsewhere)"
		return det
	}
	det.Files = len(d.Files())
	if det.Files == 0 {
		det.Status, det.Detail = NoData, "no session.jsonl.zstd files"
		return det
	}
	det.Status, det.Detail = Ready, "compressed session event logs (experimental parser)"
	return det
}

func (DeepSeek) NewParser(string, *Resume) Parser { return nil }
func (DeepSeek) ReadsPath(path string) bool       { return strings.HasSuffix(path, ".zstd") }

type dshEvent struct {
	Type          string `json:"type"`
	Time          int64  `json:"time"`
	ID            string `json:"id"`
	CWD           string `json:"cwd"`
	ParentSession string `json:"parentSession"`
	Data          *struct {
		Message *struct {
			ID     string `json:"id"`
			Source *struct {
				Model string `json:"model"`
			} `json:"source"`
		} `json:"message"`
		Usage *struct {
			Input      *int64 `json:"inputTokens"`
			Output     *int64 `json:"outputTokens"`
			CacheRead  *int64 `json:"cacheReadTokens"`
			CacheWrite *int64 `json:"cacheWriteTokens"`
			Reasoning  *int64 `json:"reasoningTokens"`
		} `json:"usage"`
	} `json:"data"`
}

func (DeepSeek) ParsePath(ctx context.Context, path string) ([]model.Event, model.ParseStats, error) {
	var st model.ParseStats
	f, err := os.Open(path)
	if err != nil {
		return nil, st, err
	}
	defer f.Close()
	// A hostile archive can expand a few KB into gigabytes: bound the window,
	// the decoder memory and the decompressed size.
	zr, err := zstd.NewReader(&ctxReader{ctx, f},
		zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(64<<20), zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return nil, st, err
	}
	defer zr.Close()
	lr := &limitedReader{r: zr, n: maxDecompressed}

	var evs []model.Event
	seen := map[string]bool{}
	session, project := filepath.Base(filepath.Dir(path)), ""
	_, err = ReadLines(lr, &st, true, func(line []byte) {
		st.Lines++
		var ev dshEvent
		if json.Unmarshal(line, &ev) != nil {
			st.Malformed++
			return
		}
		switch ev.Type {
		case "session":
			session = firstNonEmpty(ev.ID, session)
			project = firstNonEmpty(ev.CWD, project)
		case "assistant/message":
			if ev.Data == nil || ev.Data.Message == nil || ev.Data.Usage == nil || ev.Time <= 0 {
				return
			}
			id := ev.Data.Message.ID
			if id == "" || seen[id] {
				return
			}
			seen[id] = true
			mdl := "unknown"
			if s := ev.Data.Message.Source; s != nil && s.Model != "" {
				mdl = s.Model
			}
			u := ev.Data.Usage
			out := clamp(val(u.Output), &st) // once: each clamp call counts an anomaly
			reasoning := clamp(val(u.Reasoning), &st)
			e := model.Event{Provider: "deepseek", Model: mdl, Session: session, Project: project, Time: time.UnixMilli(ev.Time).UTC(),
				Key: "deepseek:" + id, Input: clamp(val(u.Input), &st), Output: out,
				CacheRead: clamp(val(u.CacheRead), &st), CacheWrite: clamp(val(u.CacheWrite), &st), Reasoning: min(reasoning, out)}
			if u.CacheRead != nil {
				e.Has |= model.HasCacheRead
			}
			if u.CacheWrite != nil {
				e.Has |= model.HasCacheWrite
			}
			if u.Reasoning != nil {
				e.Has |= model.HasReasoning
			}
			evs = append(evs, e)
		}
	})
	if lr.hit {
		st.Oversized++ // the archive expands beyond the limit: what was read is kept
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil // a session still being written, or half copied: keep what decoded
	}
	return evs, st, err
}

// maxDecompressed bounds what one archive may expand to.
var maxDecompressed int64 = 1 << 30

// limitedReader stops after n bytes and remembers that it did.
type limitedReader struct {
	r   io.Reader
	n   int64
	hit bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		var one [1]byte
		if n, _ := l.r.Read(one[:]); n > 0 {
			l.hit = true
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// ctxReader aborts a long read when the scan is cancelled.
type ctxReader struct {
	ctx context.Context
	f   *os.File
}

func (r *ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.f.Read(p)
}

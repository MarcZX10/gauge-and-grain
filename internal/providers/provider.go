// Package providers contains one adapter per tool: where its logs live, how
// to parse them, and which assumptions that parser makes.
package providers

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Status of a provider on this machine.
type Status int

const (
	NotFound    Status = iota // no data directory
	NoData                    // directory exists but no readable session files
	Ready                     // files found and parseable
	Unsupported               // data found but this release cannot parse it
)

func (s Status) String() string {
	return [...]string{"not found", "no usage files", "ready", "found, not supported yet"}[s]
}

// Detection explains what was found for one provider.
type Detection struct {
	Status Status
	Dirs   []string // directories searched
	Files  int
	Detail string // human explanation, shown by `gg sources`
}

// Provider is implemented once per tool.
type Provider interface {
	ID() string
	Name() string
	// ParserVersion must be bumped whenever parsing output could change;
	// it is part of the cache key.
	ParserVersion() int
	Detect() Detection
	Files() []string
	// NewParser starts parsing one file. When resume is non-nil the parser
	// continues from a previous run (see Resume) instead of from scratch.
	NewParser(path string, resume *Resume) Parser
	// Fields documents which fields the source reports, for the report.
	Fields() Fields
}

// Parser consumes one file line by line. It is created per file and must be
// safe to resume: State() captures everything beyond the emitted events
// that is needed to continue after the last consumed line.
type Parser interface {
	Line(line []byte)
	Result() ([]model.Event, model.ParseStats)
	State() []byte
}

// Resume carries a previous run's output for an append-only log.
type Resume struct {
	Events []model.Event // events emitted so far (keys retained)
	Stats  model.ParseStats
	State  []byte
}

// ParseAll runs a parser over a whole reader. Used by tests and tools.
func ParseAll(p Provider, path string, r io.Reader) ([]model.Event, model.ParseStats, error) {
	ps := p.NewParser(path, nil)
	var st model.ParseStats
	_, err := ReadLines(r, &st, true, ps.Line)
	evs, pst := ps.Result()
	pst.Oversized += st.Oversized
	return evs, pst, err
}

// Fields states what a provider can report. Used to keep comparisons honest.
type Fields struct {
	CacheRead, CacheWrite, Reasoning bool
	Note                             string
}

// All returns every known provider in display order.
func All() []Provider { return []Provider{Claude{}, Codex{}, Gemini{}} }

// ByID finds a provider.
func ByID(id string) Provider {
	for _, p := range All() {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// maxLine is the largest single log line we will parse. Larger lines (for
// example inlined images) are skipped and counted, never silently dropped.
const maxLine = 64 << 20

// ReadLines calls fn for every complete line without buffering the file and
// returns how many bytes were consumed. A final line without a newline is
// consumed only when allowPartial is set; otherwise it is left for the next
// run, because a writer may still be in the middle of it.
func ReadLines(r io.Reader, st *model.ParseStats, allowPartial bool, fn func(line []byte)) (int64, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	var buf []byte
	var consumed, pending int64
	skipping := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			pending += int64(len(chunk))
			if !skipping {
				if len(buf)+len(chunk) > maxLine {
					skipping, buf = true, buf[:0]
				} else {
					buf = append(buf, chunk...)
				}
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		complete := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		if pending > 0 && (complete || (err == io.EOF && allowPartial)) {
			if skipping {
				st.Oversized++
			} else if t := bytes.TrimSpace(buf); len(t) > 0 {
				fn(t)
			}
			consumed += pending
			pending, buf, skipping = 0, buf[:0], false
		}
		if err != nil {
			if err == io.EOF {
				return consumed, nil
			}
			return consumed, err
		}
	}
}

// clamp makes counters safe: negatives and absurd values (> 1e12) are
// treated as 0 and counted as anomalies instead of corrupting totals.
func clamp(v int64, st *model.ParseStats) int64 {
	if v < 0 || v > 1e12 {
		st.Anomalies++
		return 0
	}
	return v
}

func envDirs(env string) []string {
	var out []string
	for _, d := range strings.Split(os.Getenv(env), string(os.PathListSeparator)) {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// walkJSONL lists *.jsonl files below dirs.
func walkJSONL(dirs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, reported via Detect
			}
			if !de.IsDir() && strings.HasSuffix(p, ".jsonl") && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

func existingDirs(c []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range c {
		if st, err := os.Stat(d); err == nil && st.IsDir() && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

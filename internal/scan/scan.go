// Package scan walks provider files, parses them in parallel and caches the
// per-file result so later runs only re-read files that changed.
package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// schemaVersion invalidates every cache entry when the cached structures
// (model.Event) change shape. Parser changes bump Provider.ParserVersion.
const schemaVersion = 2

// fpLen is how many bytes at the start of a log, and just before the
// resume point, are fingerprinted to prove the file only grew.
const fpLen = 4096

// Progress is reported while scanning.
type Progress struct {
	Done, Total int
	File        string
	Bytes       int64
}

// Result is the outcome of one scan.
type Result struct {
	Events    []model.Event // de-duplicated across all files
	Stats     model.ParseStats
	Files     map[string]int // provider -> files seen
	Cached    int            // files served from cache
	Parsed    int            // files parsed this run
	Errors    []string       // unreadable files (never fatal)
	Duplicate int            // records dropped as duplicates
	Cancelled bool
}

// Options configure a scan.
type Options struct {
	Providers []providers.Provider
	CacheDir  string // "" disables the cache
	Progress  func(Progress)
}

type entry struct {
	Schema  int
	Parser  int
	Size    int64 // file size when scanned
	ModNano int64
	Offset  int64  // bytes consumed: end of the last complete line
	Head    []byte // hash of the first bytes
	Tail    []byte // hash of the bytes just before Offset
	State   []byte // parser state for resuming
	Events  []model.Event
	Stats   model.ParseStats
}

type job struct {
	p    providers.Provider
	path string
}

func cachePath(dir string, p providers.Provider, path string) string {
	h := sha256.Sum256([]byte(p.ID() + "\x00" + path))
	return filepath.Join(dir, p.ID()+"-"+hex.EncodeToString(h[:12])+".gob")
}

func loadEntry(file string) (*entry, bool) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var e entry
	if gob.NewDecoder(f).Decode(&e) != nil {
		return nil, false // corrupt cache entries are simply re-parsed
	}
	return &e, true
}

func saveEntry(file string, e *entry) {
	tmp := file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	err = gob.NewEncoder(f).Encode(e)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return
	}
	os.Rename(tmp, file) // atomic: a killed scan never leaves a half-written entry
}

// Run scans all providers. It stops promptly when ctx is cancelled; the
// partial result is returned with Cancelled set and completed files stay
// cached, so the next run resumes where this one stopped.
func Run(ctx context.Context, o Options) *Result {
	res := &Result{Files: map[string]int{}}
	if o.CacheDir != "" {
		_ = os.MkdirAll(o.CacheDir, 0o755)
	}
	var jobs []job
	for _, p := range o.Providers {
		fs := p.Files()
		res.Files[p.ID()] = len(fs)
		for _, f := range fs {
			jobs = append(jobs, job{p, f})
		}
	}
	total := len(jobs)
	var done, cached, parsed atomic.Int64
	var bytesDone atomic.Int64
	var mu sync.Mutex
	perJob := make([]*entry, total)

	work := make(chan int)
	var wg sync.WaitGroup
	n := max(2, runtime.NumCPU())
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				j := jobs[i]
				e, fromCache, err := processFile(ctx, j, o.CacheDir)
				if err != nil && ctx.Err() != nil {
					// cancelled mid-file: not a file error
				} else if err != nil {
					mu.Lock()
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", j.path, err))
					mu.Unlock()
				} else {
					perJob[i] = e
					bytesDone.Add(e.Size)
					if fromCache {
						cached.Add(1)
					} else {
						parsed.Add(1)
					}
				}
				d := int(done.Add(1))
				if o.Progress != nil {
					o.Progress(Progress{Done: d, Total: total, File: j.path, Bytes: bytesDone.Load()})
				}
			}
		}()
	}
feed:
	for i := range jobs {
		select {
		case <-ctx.Done():
			res.Cancelled = true
			break feed
		case work <- i:
		}
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		res.Cancelled = true
	}
	res.Cached, res.Parsed = int(cached.Load()), int(parsed.Load())

	// Global de-duplication: the same call can appear in several files
	// (resumed or forked sessions, archived copies).
	// Memory: dedupe on a 64-bit hash of the key (collision odds ~1e-7 at
	// 5M events), intern repeated strings and drop the key afterwards.
	seed := maphash.MakeSeed()
	idx := map[uint64]int32{}
	intern := map[string]string{}
	in := func(s string) string {
		if v, ok := intern[s]; ok {
			return v
		}
		intern[s] = s
		return s
	}
	for i, e := range perJob {
		if e == nil {
			continue
		}
		res.Stats.Add(e.Stats)
		for k := range e.Events {
			ev := e.Events[k]
			h := maphash.String(seed, ev.Key)
			if at, ok := idx[h]; ok {
				res.Events[at].Merge(&ev)
				res.Duplicate++
				continue
			}
			idx[h] = int32(len(res.Events))
			ev.Key = ""
			ev.Provider, ev.Model, ev.Session, ev.Project = in(ev.Provider), in(ev.Model), in(ev.Session), in(ev.Project)
			res.Events = append(res.Events, ev)
		}
		perJob[i] = nil // let the GC reclaim this file's records
	}
	return res
}

func fingerprint(f *os.File, from, n int64) []byte {
	if n <= 0 || from < 0 {
		return nil
	}
	buf := make([]byte, n)
	m, err := f.ReadAt(buf, from)
	if err != nil && m != int(n) {
		return nil
	}
	h := sha256.Sum256(buf)
	return h[:]
}

func eq(a, b []byte) bool { return a != nil && string(a) == string(b) }

// processFile returns the file's events, from the cache when the file is
// unchanged, resuming after the cached offset when it has only grown, and
// parsing from scratch otherwise.
func processFile(ctx context.Context, j job, cacheDir string) (*entry, bool, error) {
	st, err := os.Stat(j.path)
	if err != nil {
		return nil, false, err
	}
	var cp string
	var prev *entry
	if cacheDir != "" {
		cp = cachePath(cacheDir, j.p, j.path)
		if c, ok := loadEntry(cp); ok && c.Schema == schemaVersion && c.Parser == j.p.ParserVersion() {
			size, mod := st.Size(), st.ModTime().UnixNano()
			if pr, ok := j.p.(providers.PathReader); ok && pr.ReadsPath(j.path) {
				ws, wm := sidecar(j.path)
				size, mod = size+ws, max(mod, wm)
			}
			if c.Size == size && c.ModNano == mod {
				return c, true, nil
			}
			prev = c
		}
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	f, err := os.Open(j.path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var resume *providers.Resume
	var start int64
	if prev != nil && prev.Offset > 0 && prev.Offset <= st.Size() &&
		eq(prev.Head, fingerprint(f, 0, min(fpLen, prev.Offset))) &&
		eq(prev.Tail, fingerprint(f, max(0, prev.Offset-fpLen), min(fpLen, prev.Offset))) {
		resume = &providers.Resume{Events: prev.Events, Stats: prev.Stats, State: prev.State}
		start = prev.Offset
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, false, err
	}
	if pr, ok := j.p.(providers.PathReader); ok && pr.ReadsPath(j.path) {
		return pathReader(ctx, j, pr, st, cp)
	}
	if wf, ok := j.p.(providers.WholeFiler); ok && wf.WholeFile(j.path) {
		return wholeFile(ctx, j, f, st, cp)
	}
	ps := j.p.NewParser(j.path, resume)
	var rs model.ParseStats
	// A file idle for a couple of minutes will not grow a half-written
	// last line, so an unterminated final record is complete.
	idle := time.Since(st.ModTime()) > 2*time.Minute
	n, err := providers.ReadLines(&ctxReader{ctx, f}, &rs, idle, ps.Line)
	if err != nil {
		return nil, false, err
	}
	evs, stats := ps.Result()
	stats.Oversized += rs.Oversized
	off := start + n
	e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size(), ModNano: st.ModTime().UnixNano(),
		Offset: off, State: ps.State(), Events: evs, Stats: stats}
	e.Head = fingerprint(f, 0, min(fpLen, off))
	e.Tail = fingerprint(f, max(0, off-fpLen), min(fpLen, off))
	if cp != "" {
		saveEntry(cp, e)
	}
	return e, false, nil
}

// walSize and walMod fold a SQLite "-wal" sidecar into a database's cache
// key: recent writes live there until a checkpoint, so the main file alone
// can look unchanged while its content has moved on.
func sidecar(path string) (size, mod int64) {
	if ws, err := os.Stat(path + "-wal"); err == nil {
		return ws.Size(), ws.ModTime().UnixNano()
	}
	return 0, 0
}

// pathReader lets a provider open its own file (SQLite).
func pathReader(ctx context.Context, j job, pr providers.PathReader, st os.FileInfo, cp string) (*entry, bool, error) {
	evs, stats, err := pr.ParsePath(ctx, j.path)
	if err != nil {
		return nil, false, err
	}
	ws, wm := sidecar(j.path)
	e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size() + ws, ModNano: max(st.ModTime().UnixNano(), wm), Events: evs, Stats: stats}
	if cp != "" {
		saveEntry(cp, e)
	}
	return e, false, nil
}

// maxWholeFile bounds a single-document log. Larger files are skipped and
// counted as oversized rather than read into memory.
const maxWholeFile = 256 << 20

// wholeFile parses a single-document log (one JSON value per file).
func wholeFile(ctx context.Context, j job, f *os.File, st os.FileInfo, cp string) (*entry, bool, error) {
	ps := j.p.NewParser(j.path, nil)
	var stats model.ParseStats
	if st.Size() > maxWholeFile {
		stats.Oversized++
		e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size(), ModNano: st.ModTime().UnixNano(), Stats: stats}
		if cp != "" {
			saveEntry(cp, e)
		}
		return e, false, nil
	}
	data, err := io.ReadAll(&ctxReader{ctx, f})
	if err != nil {
		return nil, false, err
	}
	if t := bytes.TrimSpace(data); len(t) > 0 {
		ps.Line(t)
	}
	evs, stats2 := ps.Result()
	stats2.Oversized += stats.Oversized
	// Offset 0: a changed document is always re-read in full, never resumed.
	e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size(), ModNano: st.ModTime().UnixNano(), Events: evs, Stats: stats2}
	if cp != "" {
		saveEntry(cp, e)
	}
	return e, false, nil
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

// ClearCache removes every cached entry.
func ClearCache(dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".gob" || filepath.Ext(e.Name()) == ".tmp" {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n, nil
}

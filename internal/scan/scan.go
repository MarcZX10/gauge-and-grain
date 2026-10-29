// Package scan walks provider files, parses them in parallel and caches the
// per-file result so later runs only re-read files that changed.
package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
	"github.com/MarcZX10/gauge-and-grain/internal/providers"
)

// schemaVersion invalidates every cache entry when the cached structures
// (model.Event) change shape. Parser changes bump Provider.ParserVersion.
const schemaVersion = 3

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
	Stale     int            // files served from an older parser's cache (AllowStale)
	Cancelled bool
}

// Options configure a scan.
type Options struct {
	Providers []providers.Provider
	CacheDir  string // "" disables the cache
	Progress  func(Progress)
	// AllowStale serves files that did not change from a cache written by an
	// older parser version, instead of re-parsing them. The result is then
	// complete for usage but may lack what the newer parser adds (such as
	// tool calls): Result.Stale counts those files, and a scan without
	// AllowStale refreshes them. The dashboard uses it to show data at once
	// after an upgrade, rather than waiting for every log to be read again.
	AllowStale bool
}

type entry struct {
	Schema  int
	Parser  int
	Size    int64 // file size when scanned
	ModNano int64
	Extra   uint64 // signature of the provider's other key files (SQLite -wal, side files)
	Partial bool   // an unterminated last line was left unread (the file was still young)
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

// maxCacheEntry bounds what is decoded from a cache file: gob would
// otherwise allocate whatever a damaged or planted file claims.
const maxCacheEntry = 512 << 20

func loadEntry(file string) (*entry, bool) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCacheEntry {
		return nil, false
	}
	var e entry
	if gob.NewDecoder(f).Decode(&e) != nil {
		return nil, false // corrupt cache entries are simply re-parsed
	}
	return &e, true
}

// saveEntry writes atomically (a killed scan never leaves a half-written
// entry) through a private temporary file, so two gg processes never share
// one, and with owner-only permissions: the entries hold project paths.
func saveEntry(file string, e *entry) {
	f, err := os.CreateTemp(filepath.Dir(file), "gg-*.tmp")
	if err != nil {
		return
	}
	tmp := f.Name()
	err = gob.NewEncoder(f).Encode(e)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp, file) != nil {
		os.Remove(tmp)
	}
}

// sanitize bounds and cleans every string taken from a log before it is
// cached, counted or printed (see model.Clean).
func sanitize(evs []model.Event) {
	for i := range evs {
		e := &evs[i]
		e.Model, e.Session, e.Project = model.Clean(e.Model), model.Clean(e.Session), model.Clean(e.Project)
		e.Key = model.BoundKey(e.Key)
	}
}

// Run scans all providers. It stops promptly when ctx is cancelled; the
// partial result is returned with Cancelled set and completed files stay
// cached, so the next run resumes where this one stopped.
func Run(ctx context.Context, o Options) *Result {
	res := &Result{Files: map[string]int{}}
	if o.CacheDir != "" {
		_ = os.MkdirAll(o.CacheDir, 0o700)
		_ = os.Chmod(o.CacheDir, 0o700) // a cache made by an older version was world-readable
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
	var done, cached, parsed, stale atomic.Int64
	var bytesDone atomic.Int64
	var mu, progMu sync.Mutex

	type finished struct {
		i int
		e *entry
	}
	out := make(chan finished)
	merged := make(chan struct{})
	go func() { // the only goroutine that touches res.Events / res.Stats / res.Duplicate
		defer close(merged)
		m := newMerger(res)
		pending := map[int]*entry{}
		next := 0
		for f := range out {
			pending[f.i] = f.e
			for {
				e, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				next++
				m.add(e)
			}
		}
		// Cancelled: jobs that were never fed leave gaps; take what is left in order.
		rest := make([]int, 0, len(pending))
		for i := range pending {
			rest = append(rest, i)
		}
		sort.Ints(rest)
		for _, i := range rest {
			m.add(pending[i])
		}
	}()

	work := make(chan int)
	var wg sync.WaitGroup
	n := max(2, runtime.NumCPU())
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				j := jobs[i]
				e, fromCache, err := processFile(ctx, j, o.CacheDir, o.AllowStale)
				if err != nil && ctx.Err() != nil {
					e = nil // cancelled mid-file: not a file error
				} else if err != nil {
					mu.Lock()
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", model.Clean(j.path), err))
					mu.Unlock()
					e = nil
				} else {
					bytesDone.Add(e.Size)
					if fromCache {
						cached.Add(1)
						if e.Parser != j.p.ParserVersion() {
							stale.Add(1)
						}
					} else {
						parsed.Add(1)
					}
				}
				out <- finished{i, e}
				if o.Progress != nil {
					progMu.Lock() // Progress is documented as called from one goroutine at a time
					d := int(done.Add(1))
					o.Progress(Progress{Done: d, Total: total, File: j.path, Bytes: bytesDone.Load()})
					progMu.Unlock()
				} else {
					done.Add(1)
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
	close(out)
	<-merged
	if ctx.Err() != nil {
		res.Cancelled = true
	}
	res.Cached, res.Parsed, res.Stale = int(cached.Load()), int(parsed.Load()), int(stale.Load())
	if !res.Cancelled && o.CacheDir != "" {
		prune(o.CacheDir, o.Providers, jobs)
	}
	return res
}

// merger folds per-file events into one de-duplicated list as files finish,
// so decoded entries are released early instead of all being held at once.
//
// Global de-duplication: the same call can appear in several files (resumed
// or forked sessions, archived copies). Memory: dedupe on a 64-bit hash of the
// key (collision odds ~1e-7 at 5M events), intern repeated strings and drop
// the key afterwards.
type merger struct {
	res    *Result
	seed   maphash.Seed
	idx    map[uint64]int32
	intern map[string]string
}

func newMerger(res *Result) *merger {
	return &merger{res: res, seed: maphash.MakeSeed(), idx: map[uint64]int32{}, intern: map[string]string{}}
}

// in cleans an untrusted string once (see model.Clean: this also covers cache
// entries written by older versions) and returns one shared copy.
func (m *merger) in(s string) string {
	if v, ok := m.intern[s]; ok {
		return v
	}
	v := model.Clean(s)
	if v == s {
		m.intern[s] = s
		return s
	}
	// Only strings that needed cleaning get here (never from entries this
	// version wrote); the raw original is not kept as a map key.
	if w, ok := m.intern[v]; ok {
		return w
	}
	m.intern[v] = v
	return v
}

func (m *merger) add(e *entry) {
	if e == nil {
		return
	}
	res := m.res
	res.Stats.Add(e.Stats)
	if res.Events == nil && len(e.Events) > 0 {
		res.Events = make([]model.Event, 0, max(1024, len(e.Events)*4))
	}
	for k := range e.Events {
		ev := e.Events[k]
		h := maphash.String(m.seed, ev.Key)
		if at, ok := m.idx[h]; ok {
			res.Events[at].Merge(&ev)
			res.Duplicate++
			continue
		}
		m.idx[h] = int32(len(res.Events))
		ev.Key = ""
		ev.Provider, ev.Model, ev.Session, ev.Project = m.in(ev.Provider), m.in(ev.Model), m.in(ev.Session), m.in(ev.Project)
		res.Events = append(res.Events, ev)
	}
}

// cacheName matches the cache files gg itself writes.
var cacheName = regexp.MustCompile(`^[a-z0-9]+-[0-9a-f]{24}\.gob$`)

// prune deletes cache entries of the scanned providers whose log no longer
// exists (a tool rotated or removed it), so the cache cannot grow forever.
func prune(dir string, ps []providers.Provider, jobs []job) {
	live := map[string]bool{}
	for _, j := range jobs {
		live[filepath.Base(cachePath(dir, j.p, j.path))] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if live[name] || !cacheName.MatchString(name) {
			continue
		}
		for _, p := range ps {
			if strings.HasPrefix(name, p.ID()+"-") && len(name) == len(p.ID())+1+24+4 {
				os.Remove(filepath.Join(dir, name))
				break
			}
		}
	}
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

// idleAfter is how long a log must stay untouched before an unterminated
// last line is taken as complete: a writer would have finished it by then.
var idleAfter = 2 * time.Minute

// keySig folds the size and modification time of a provider's other key
// files into one number. A missing file counts too (its appearance changes
// the signature). It is taken BEFORE parsing, so a write that lands while
// the file is being read leaves the entry stale and is picked up next run.
func keySig(p providers.Provider, path string) uint64 {
	kf, ok := p.(providers.KeyFiler)
	if !ok {
		return 0
	}
	h := fnv.New64a()
	for _, f := range kf.KeyFiles(path) {
		var b [17]byte
		if fi, err := os.Stat(f); err == nil {
			b[0] = 1
			putU64(b[1:9], uint64(fi.Size()))
			putU64(b[9:17], uint64(fi.ModTime().UnixNano()))
		}
		h.Write([]byte(f))
		h.Write(b[:])
	}
	return h.Sum64()
}

func putU64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

// processFile returns the file's events, from the cache when the file is
// unchanged, resuming after the cached offset when it has only grown, and
// parsing from scratch otherwise.
func processFile(ctx context.Context, j job, cacheDir string, allowStale bool) (*entry, bool, error) {
	st, err := os.Stat(j.path)
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() { // a FIFO or device would block or never end
		return nil, false, errors.New("not a regular file")
	}
	sig := keySig(j.p, j.path)
	var cp string
	var prev, staleC *entry
	if cacheDir != "" {
		cp = cachePath(cacheDir, j.p, j.path)
		if c, ok := loadEntry(cp); ok && c.Schema == schemaVersion {
			unchanged := c.Size == st.Size() && c.ModNano == st.ModTime().UnixNano() && c.Extra == sig
			if c.Parser == j.p.ParserVersion() {
				// A last line without a newline was left for later because the
				// writer might still be in the middle of it. Once the file has
				// been quiet long enough it is complete, so re-read the tail.
				if unchanged && !(c.Partial && time.Since(st.ModTime()) > idleAfter) {
					return c, true, nil
				}
				prev = c
			} else if allowStale {
				// Written by an older parser. An unchanged log's usage is still
				// right, so it is served now, marked stale (Options.AllowStale).
				// A log that only grew is served up to what the cache holds,
				// once its beginning is confirmed unchanged below.
				if unchanged {
					return c, true, nil
				}
				staleC = c
			}
		}
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if pr, ok := j.p.(providers.PathReader); ok && pr.ReadsPath(j.path) {
		return pathReader(ctx, j, pr, st, sig, cp)
	}
	f, err := os.Open(j.path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if fst, err := f.Stat(); err != nil || !fst.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	if c := staleC; allowStale && c != nil && c.Offset > 0 && c.Offset <= st.Size() &&
		eq(c.Head, fingerprint(f, 0, min(fpLen, c.Offset))) &&
		eq(c.Tail, fingerprint(f, max(0, c.Offset-fpLen), min(fpLen, c.Offset))) {
		return c, true, nil // the part the cache holds is unchanged: serve it, stale
	}

	if wf, ok := j.p.(providers.WholeFiler); ok && wf.WholeFile(j.path) {
		return wholeFile(ctx, j, f, st, sig, cp)
	}
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
	ps := j.p.NewParser(j.path, resume)
	var rs model.ParseStats
	idle := time.Since(st.ModTime()) > idleAfter
	n, err := providers.ReadLines(&ctxReader{ctx, f}, &rs, idle, ps.Line)
	if err != nil {
		return nil, false, err
	}
	evs, stats := ps.Result()
	stats.Oversized += rs.Oversized
	off := start + n
	sanitize(evs)
	e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size(), ModNano: st.ModTime().UnixNano(), Extra: sig,
		Partial: !idle && off < st.Size(), Offset: off, State: ps.State(), Events: evs, Stats: stats}
	e.Head = fingerprint(f, 0, min(fpLen, off))
	e.Tail = fingerprint(f, max(0, off-fpLen), min(fpLen, off))
	if cp != "" {
		saveEntry(cp, e)
	}
	return e, false, nil
}

// pathReader lets a provider open its own file (SQLite). The cache key was
// taken before the read (see keySig and the stat in processFile).
func pathReader(ctx context.Context, j job, pr providers.PathReader, st os.FileInfo, sig uint64, cp string) (*entry, bool, error) {
	evs, stats, err := pr.ParsePath(ctx, j.path)
	if err != nil {
		return nil, false, err
	}
	sanitize(evs)
	e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size(), ModNano: st.ModTime().UnixNano(), Extra: sig, Events: evs, Stats: stats}
	if cp != "" {
		saveEntry(cp, e)
	}
	return e, false, nil
}

// maxWholeFile bounds a single-document log. Larger files are skipped and
// counted as oversized rather than read into memory.
const maxWholeFile = 256 << 20

// wholeFile parses a single-document log (one JSON value per file).
func wholeFile(ctx context.Context, j job, f *os.File, st os.FileInfo, sig uint64, cp string) (*entry, bool, error) {
	ps := j.p.NewParser(j.path, nil)
	var over model.ParseStats
	data, err := io.ReadAll(io.LimitReader(&ctxReader{ctx, f}, maxWholeFile+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxWholeFile {
		data = nil
		over.Oversized++
	}
	if t := bytes.TrimSpace(data); len(t) > 0 {
		ps.Line(t)
	}
	evs, stats := ps.Result()
	stats.Oversized += over.Oversized
	sanitize(evs)
	// Offset 0: a changed document is always re-read in full, never resumed.
	e := &entry{Schema: schemaVersion, Parser: j.p.ParserVersion(), Size: st.Size(), ModNano: st.ModTime().UnixNano(), Extra: sig, Events: evs, Stats: stats}
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

// ClearCache removes the cache entries gg wrote (and its temporary files),
// and nothing else: GG_CACHE_DIR may point at a directory with other files.
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
		name := e.Name()
		if cacheName.MatchString(name) || (strings.HasPrefix(name, "gg-") && strings.HasSuffix(name, ".tmp")) || strings.HasSuffix(name, ".gob.tmp") {
			if os.Remove(filepath.Join(dir, name)) == nil {
				n++
			}
		}
	}
	return n, nil
}

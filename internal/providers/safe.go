package providers

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Limits for files read whole into memory. Logs are hostile-tolerant: a file
// above its limit is skipped and reported, never read.
const (
	maxSmallFile = 64 << 20 // side files next to a log (task metadata, history)
)

// ErrTooLarge is returned by ReadRegular for a file above its limit.
var ErrTooLarge = errors.New("file too large")

// isRegular reports whether a directory entry is a regular file, following a
// symlink to its target. FIFOs, sockets and devices (a log named x.jsonl that
// is /dev/zero, or a named pipe, would hang the scan) are rejected.
func isRegular(p string, de os.DirEntry) bool {
	if de.Type().IsRegular() {
		return true
	}
	if de.Type()&os.ModeSymlink != 0 {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular()
	}
	return false
}

// ReadRegular reads a regular file of at most max bytes.
func ReadRegular(path string, max int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > max {
		return nil, ErrTooLarge
	}
	f, err := openNoBlock(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fst, err := f.Stat(); err != nil || !fst.Mode().IsRegular() { // replaced since the stat
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max { // grew while reading
		return nil, ErrTooLarge
	}
	return b, nil
}

// resolve follows symlinks in a root directory: WalkDir would otherwise see
// a linked ~/.claude/projects as an empty file.
func resolve(d string) string {
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return d
}

// listFiles returns the regular files directly inside dir whose names match
// the glob pattern. Unlike filepath.Glob, characters such as [ or * in dir
// itself are literal (a home directory called /home/x[1] must still work).
func listFiles(dir, pattern string) []string {
	ents, err := os.ReadDir(resolve(dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if ok, _ := filepath.Match(pattern, e.Name()); !ok {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if isRegular(p, e) {
			out = append(out, p)
		}
	}
	return out
}

// subdirs lists the directories directly inside dir.
func subdirs(dir string) []string {
	ents, err := os.ReadDir(resolve(dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			out = append(out, p)
		} else if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				out = append(out, p)
			}
		}
	}
	return out
}

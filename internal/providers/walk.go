package providers

import (
	"os"
	"path/filepath"
)

// walkExt appends the regular files below dirs that satisfy keep. Unreadable
// subtrees are skipped and counted as discovery gaps (Detect reports what was
// found); a root that is a symlink is followed, and so is a symlinked
// subdirectory, wherever it points. Each directory is walked once, by its
// resolved path, so a link back to an ancestor ends. FIFOs, devices and
// sockets are never listed.
func walkExt(dirs []string, keep func(path string) bool, out *[]string) error {
	seen := map[string]bool{}
	visited := map[string]bool{}
	for _, d := range dirs {
		root := resolve(d)
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			gaps.Add(1)
			continue
		}
		walkTree(root, real, keep, seen, visited, out)
	}
	return nil
}

// walkTree lists dir, whose resolved path is real. Only the roots and the
// symlinks met below them are resolved: a plain subdirectory's resolved path
// is its parent's plus its name, so it costs no extra path lookups.
func walkTree(dir, real string, keep func(path string) bool, seen, visited map[string]bool, out *[]string) {
	if visited[real] {
		return
	}
	visited[real] = true
	ents, err := os.ReadDir(real)
	if err != nil {
		gaps.Add(1)
		return
	}
	for _, de := range ents {
		p := filepath.Join(dir, de.Name())
		switch {
		case de.IsDir():
			walkTree(p, filepath.Join(real, de.Name()), keep, seen, visited, out)
		case de.Type()&os.ModeSymlink != 0 && isDirEntry(p, de):
			r, err := filepath.EvalSymlinks(p)
			if err != nil {
				gaps.Add(1)
				continue
			}
			walkTree(p, r, keep, seen, visited, out)
		case keep(p) && isRegular(p, de) && !seen[p]:
			seen[p] = true
			*out = append(*out, p)
		}
	}
}

// isDirEntry reports whether a directory entry is a directory, following a
// symlink to its target.
func isDirEntry(p string, de os.DirEntry) bool {
	if de.IsDir() {
		return true
	}
	if de.Type()&os.ModeSymlink != 0 {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
	return false
}

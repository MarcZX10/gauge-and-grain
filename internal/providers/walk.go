package providers

import (
	"os"
	"path/filepath"
)

// walkExt appends the regular files below dirs that satisfy keep. Unreadable
// subtrees are skipped (Detect reports what was found); a root that is a
// symlink is followed; FIFOs, devices and sockets are never listed.
func walkExt(dirs []string, keep func(path string) bool, out *[]string) error {
	seen := map[string]bool{}
	for _, d := range dirs {
		root := resolve(d)
		_ = filepath.WalkDir(root, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !de.IsDir() && keep(p) && isRegular(p, de) && !seen[p] {
				seen[p] = true
				*out = append(*out, p)
			}
			return nil
		})
	}
	return nil
}

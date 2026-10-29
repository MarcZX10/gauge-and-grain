package providers

import (
	"os"
	"path/filepath"
)

// walkExt appends files below dirs that satisfy keep. Unreadable subtrees
// are skipped (Detect reports what was found).
func walkExt(dirs []string, keep func(path string) bool, out *[]string) error {
	seen := map[string]bool{}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !de.IsDir() && keep(p) && !seen[p] {
				seen[p] = true
				*out = append(*out, p)
			}
			return nil
		})
	}
	return nil
}

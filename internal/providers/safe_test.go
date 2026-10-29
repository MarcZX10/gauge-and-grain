package providers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadRegularLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadRegular(p, 100); err != nil || len(b) != 100 {
		t.Errorf("at the limit: %v %d", err, len(b))
	}
	if _, err := ReadRegular(p, 99); err != ErrTooLarge {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := ReadRegular(t.TempDir(), 99); err == nil {
		t.Error("a directory is not a regular file")
	}
}

// Glob metacharacters in the home directory must not break discovery.
func TestListFilesTreatsDirectoryLiterally(t *testing.T) {
	name := "x[1]"
	if runtime.GOOS != "windows" { // Windows does not allow * in a file name
		name += "*"
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"opencode.db", "opencode-2.db", "other.db"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := listFiles(dir, "opencode.db"); len(got) != 1 {
		t.Errorf("%v", got)
	}
	if got := listFiles(dir, "opencode-*.db"); len(got) != 1 {
		t.Errorf("%v", got)
	}
}

func TestDeepSeekFindsSessionsUnderBracketedHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h[1]")
	sess := filepath.Join(home, "sessions", "ws", "s1")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess, "session.jsonl.zstd"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	if got := (DeepSeek{}).Files(); len(got) != 1 {
		t.Errorf("%v", got)
	}
}

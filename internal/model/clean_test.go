package model

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanRemovesTerminalControls(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus":                     "claude-opus",
		"\x1b]0;PWNED\x07\x1b[2Jname":     "]0;PWNED[2Jname", // ESC and BEL gone: no longer an escape sequence
		"a\u009b31mb":                     "a31mb",           // C1 CSI
		"tab\there\nnew":                  "tab here new",
		"rtl‮evil":                        "rtlevil",
		"bad\xffbyte":                     "bad�byte",
		"/home/ñandú/proyecto español 日本": "/home/ñandú/proyecto español 日本",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanBoundsLength(t *testing.T) {
	got := Clean(strings.Repeat("日", 5000))
	if len(got) > MaxText || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Errorf("len %d valid=%v suffix=%q", len(got), utf8.ValidString(got), got[len(got)-4:])
	}
	if s := strings.Repeat("a", MaxText); Clean(s) != s {
		t.Error("a string of exactly MaxText bytes must be kept")
	}
}

func TestBoundKey(t *testing.T) {
	if BoundKey("short") != "short" {
		t.Fatal("short keys are unchanged")
	}
	a, b := BoundKey(strings.Repeat("x", 4000)+"1"), BoundKey(strings.Repeat("x", 4000)+"2")
	if len(a) > 512 || a == b {
		t.Errorf("long keys must be bounded yet distinct: %d %v", len(a), a == b)
	}
}

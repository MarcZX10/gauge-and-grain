package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --days cannot be combined with a range: it used to be silently ignored.
func TestDaysAndRangeFlagsConflict(t *testing.T) {
	bin, home := setup(t)
	out, err := gg(t, bin, home, "report", "--days", "7", "--from", "2026-10-01")
	if err == nil || !strings.Contains(out, "cannot be combined") {
		t.Errorf("want an error, got %v: %s", err, out)
	}
}

// A prices file that was asked for and is missing is an error, not a silent default.
func TestMissingPricesFileIsAnError(t *testing.T) {
	bin, home := setup(t)
	out, err := gg(t, bin, home, "report", "--prices", filepath.Join(home, "nope.json"))
	if err == nil || !strings.Contains(out, "nope.json") {
		t.Errorf("want an error naming the file, got %v: %s", err, out)
	}
}

// An unknown flag points at the help instead of only saying it is not defined.
func TestUnknownFlagPointsAtHelp(t *testing.T) {
	bin, home := setup(t)
	out, err := gg(t, bin, home, "report", "--no-such-flag")
	if err == nil || !strings.Contains(out, "gg help") {
		t.Errorf("want a pointer to help, got %v: %s", err, out)
	}
}

// GG_COMPACT=0 is off; any other value is on.
func TestEnvFlagValues(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "FALSE": false, "1": true, "yes": true} {
		os.Setenv("GG_TEST_FLAG", v)
		if got := envFlag("GG_TEST_FLAG"); got != want {
			t.Errorf("envFlag(%q) = %v, want %v", v, got, want)
		}
	}
	os.Unsetenv("GG_TEST_FLAG")
}

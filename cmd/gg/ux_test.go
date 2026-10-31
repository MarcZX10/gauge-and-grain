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

// --from without --to is open-ended (up to today), so a start date in the
// future is an empty report, not an error. Both dates given are still compared.
func TestFromWithoutToIsOpenEnded(t *testing.T) {
	bin, home := setup(t)
	if out, err := gg(t, bin, home, "report", "--from", "2026-12-01"); err != nil || strings.Contains(out, "must not be before") {
		t.Errorf("--from alone must not fail: %v\n%s", err, out)
	}
	if out, err := gg(t, bin, home, "report", "--from", "2026-10-02", "--to", "2026-10-01"); err == nil || !strings.Contains(out, "must not be before") {
		t.Errorf("--to before --from must still fail: %v\n%s", err, out)
	}
}

// Flags that would be silently ignored are errors, each with a hint naming the
// command that accepts it. Combinations that work are checked in the unit test below.
func TestFlagsThatWouldBeIgnoredAreErrors(t *testing.T) {
	bin, home := setup(t)
	for _, c := range []struct {
		args []string
		hint string
	}{
		{[]string{"report", "--all", "--from", "2026-10-01"}, "--all cannot be combined with --from or --to"},
		{[]string{"export", "--all", "--to", "2026-10-01"}, "--all cannot be combined with --from or --to"},
		{[]string{"export", "--daily"}, "--daily only applies to `gg report`"},
		{[]string{"notes", "--daily"}, "--daily only applies to `gg report`"},
		{[]string{"export", "--notes"}, "--notes only applies to `gg report`"},
		{[]string{"prices", "--notes"}, "--notes only applies to `gg report`"},
		{[]string{"report", "--watch"}, "--watch only applies to `gg tui`"},
		{[]string{"export", "--watch"}, "--watch only applies to `gg tui`"},
		{[]string{"export", "--unpriced"}, "--unpriced only applies to `gg prices`"},
		{[]string{"report", "--unpriced"}, "--unpriced only applies to `gg prices`"},
		{[]string{"export", "--what-if", "x.json"}, "--what-if only applies to `gg report`"},
		{[]string{"tui", "--what-if", "x.json"}, "--what-if only applies to `gg report`"},
	} {
		out, err := gg(t, bin, home, c.args...)
		if err == nil || !strings.Contains(out, c.hint) {
			t.Errorf("gg %v: want error with %q, got err=%v output:\n%s", c.args, c.hint, err, out)
		}
	}
}

// Every flag is accepted by the command that uses it, and --all works with --daily/--notes.
func TestFlagsAcceptedByTheirCommand(t *testing.T) {
	for _, c := range []struct {
		cmd string
		set []string
	}{
		{"report", []string{"all", "daily"}},
		{"report", []string{"all", "notes"}},
		{"report", []string{"days", "what-if"}},
		{"tui", []string{"watch"}},
		{"prices", []string{"unpriced", "from", "to"}},
		{"export", []string{"format", "by"}},
		{"report", []string{"from"}},
	} {
		o := &opts{format: "csv", by: "model"}
		set := map[string]bool{}
		for _, s := range c.set {
			set[s] = true
		}
		if err := checkFlagUse(c.cmd, o, set); err != nil {
			t.Errorf("%s %v should be accepted: %v", c.cmd, c.set, err)
		}
	}
	if err := checkFlagUse("export", &opts{format: "json"}, map[string]bool{"by": true}); err == nil {
		t.Error("--by with json must still be rejected")
	}
}

// --unpriced uses the period and filters, and its token column has a unit.
func TestUnpricedListFollowsThePeriod(t *testing.T) {
	bin, home := setup(t)
	out, err := gg(t, bin, home, "prices", "--unpriced", "--all")
	if err != nil || !strings.Contains(out, "mystery-model-9") || !strings.Contains(out, "all time") || !strings.Contains(out, "tokens") {
		t.Errorf("whole-history unpriced list: %v\n%s", err, out)
	}
	out, err = gg(t, bin, home, "prices", "--unpriced", "--from", "2030-01-01", "--to", "2030-12-31")
	if err != nil || strings.Contains(out, "mystery-model-9") || !strings.Contains(out, "none") || !strings.Contains(out, "custom range") {
		t.Errorf("unpriced list must honour --from/--to: %v\n%s", err, out)
	}
}

// --what-if adds one line under the cost row and leaves every other line as it was.
func TestWhatIfPricesAddOneLine(t *testing.T) {
	bin, home := setup(t)
	wi := filepath.Join(t.TempDir(), "whatif.json")
	// sonnet at zero price: the what-if cost is 0, so the change is exactly -100%
	if err := os.WriteFile(wi, []byte(`{"models":[{"id":"claude-sonnet-5-5","source":"test","input":0,"output":0}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// filtered to sonnet: a partial total (an unpriced model in the range) has no change to report
	base, err := gg(t, bin, home, "report", "--all", "--model", "claude-sonnet-5-5")
	if err != nil {
		t.Fatal(err, base)
	}
	with, err := gg(t, bin, home, "report", "--all", "--model", "claude-sonnet-5-5", "--what-if", wi)
	if err != nil {
		t.Fatal(err, with)
	}
	var kept []string
	var extra []string
	for _, l := range strings.Split(with, "\n") {
		if strings.Contains(l, "What-if prices (") {
			extra = append(extra, l)
			continue
		}
		kept = append(kept, l)
	}
	if len(extra) != 1 {
		t.Fatalf("want exactly one what-if line, got %d:\n%s", len(extra), with)
	}
	if !strings.Contains(extra[0], wi) || !strings.Contains(extra[0], "$0.00") || !strings.Contains(extra[0], "-100.0% vs current") {
		t.Errorf("what-if line: %q", extra[0])
	}
	if strings.Join(kept, "\n") != base {
		t.Errorf("what-if must not change the other lines.\nwith:\n%s\nbase:\n%s", with, base)
	}
	// the line sits right after the cost row
	lines := strings.Split(with, "\n")
	for i, l := range lines {
		if strings.Contains(l, "What-if prices (") && !strings.Contains(lines[i-1], "Est. cost") {
			t.Errorf("what-if line must follow the cost row, previous line: %q", lines[i-1])
		}
	}
	// the same loader and validation as --prices: a missing file is an error that names it
	missing := filepath.Join(home, "nope.json")
	if out, err := gg(t, bin, home, "report", "--all", "--what-if", missing); err == nil || !strings.Contains(out, "nope.json") {
		t.Errorf("missing what-if file must fail naming it: %v\n%s", err, out)
	}
}

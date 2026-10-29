package providers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// These fixtures are synthetic: built from the documented format of each
// tool, NOT captured from real logs. The expected numbers are worked out by
// hand from the fixture files, so they test the stated semantics.

func parseFixture(t *testing.T, p Provider, rel string) ([]model.Event, model.ParseStats) {
	t.Helper()
	evs, st, err := ParseFile(p, filepath.Join("..", "..", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return evs, st
}

func sum(evs []model.Event) (total int64) {
	for i := range evs {
		total += evs[i].Total()
	}
	return
}

func find(evs []model.Event, model string) *model.Event {
	for i := range evs {
		if evs[i].Model == model {
			return &evs[i]
		}
	}
	return nil
}

func TestAllNewProvidersAreMarkedExperimental(t *testing.T) {
	for _, p := range []Provider{Gemini{}, Qwen{}, Pi{}, vscodeExts[0], vscodeExts[1], vscodeExts[2], vscodeExts[3], opencodeFamily[0], opencodeFamily[1], DeepSeek{}} {
		ex, ok := p.(Experimental)
		if !ok || ex.Experimental() == "" {
			t.Errorf("%s must declare itself experimental until verified against real logs", p.ID())
		}
	}
	for _, p := range []Provider{Claude{}, Codex{}} {
		if _, ok := p.(Experimental); ok {
			t.Errorf("%s is verified and must not be marked experimental", p.ID())
		}
	}
}

func TestGeminiJSONSessionSemantics(t *testing.T) {
	evs, st := parseFixture(t, Gemini{}, "gemini/tmp/proj-abc123/chats/session-1.json")
	if len(evs) != 2 {
		t.Fatalf("want 2 events (no-token message skipped), got %d", len(evs))
	}
	m2 := find(evs, "gemini-2.5-pro")
	// input 1000 includes cached 600; tool 20 is input; thoughts 50 are output; total = 1170.
	if m2.Input != 420 || m2.CacheRead != 600 || m2.Output != 150 || m2.Reasoning != 50 || m2.Total() != 1170 {
		t.Errorf("m2 normalisation: %+v", m2)
	}
	if m2.Has&model.HasCacheWrite != 0 {
		t.Error("Gemini CLI records no cache writes: must not be reported as 0")
	}
	m3 := find(evs, "gemini-2.5-flash")
	if m3.Input != 0 || m3.CacheRead != 500 || m3.Total() != 510 {
		t.Errorf("cached > prompt must be clamped to the prompt: %+v", m3)
	}
	if st.Malformed != 1 || st.Anomalies != 1 {
		t.Errorf("malformed=%d (bad timestamp) anomalies=%d (cached>input)", st.Malformed, st.Anomalies)
	}
	if m2.Session != "g-sess-1" || m2.Project != "gemini:proj-abc123" {
		t.Errorf("session/project: %q %q", m2.Session, m2.Project)
	}
}

func TestGeminiJSONLLatestRecordingWinsAndSetBlocksAreRead(t *testing.T) {
	evs, st := parseFixture(t, Gemini{}, "gemini/tmp/proj-abc123/chats/session-2.jsonl")
	if len(evs) != 2 || sum(evs) != 140+225 {
		t.Fatalf("events=%d total=%d, want 2 / 365 (a1 re-recorded with more output counts once)", len(evs), sum(evs))
	}
	if st.Malformed != 1 {
		t.Errorf("malformed=%d", st.Malformed)
	}
}

func TestQwen(t *testing.T) {
	evs, st := parseFixture(t, Qwen{}, "qwen/projects/-work-alpha/chats/q1.jsonl")
	if len(evs) != 2 || sum(evs) != 1130+55 {
		t.Fatalf("events=%d total=%d, want 2 / 1185", len(evs), sum(evs))
	}
	u2 := find(evs, "qwen3-coder-plus")
	if u2.Input+u2.CacheRead+u2.Output < 1 || st.Malformed != 1 || st.Anomalies != 1 {
		t.Errorf("malformed=%d anomalies=%d", st.Malformed, st.Anomalies)
	}
	for _, e := range evs {
		if e.Key == "qwen:q1:u2" && (e.Input != 600 || e.CacheRead != 400 || e.Output != 130 || e.Reasoning != 30) {
			t.Errorf("u2: %+v", e)
		}
		if e.Project != "qwen:-work-alpha" {
			t.Errorf("project %q", e.Project)
		}
	}
}

func TestPiTracksModelChangesAndSkipsOtherRoles(t *testing.T) {
	evs, _ := parseFixture(t, Pi{}, "pi/agent/sessions/--work-alpha--/s1.jsonl")
	if len(evs) != 2 || sum(evs) != 5510+150 {
		t.Fatalf("events=%d total=%d, want 2 / 5660 (duplicate line and toolResult ignored)", len(evs), sum(evs))
	}
	a := find(evs, "anthropic/claude-sonnet-5-5")
	if a == nil || a.Input != 10 || a.CacheRead != 5000 || a.CacheWrite != 300 || a.Output != 200 || a.Project != "/work/alpha" || a.Session != "pi-1" {
		t.Errorf("anthropic message: %+v", a)
	}
	b := find(evs, "openai/gpt-5.4")
	if b == nil || b.Has != 0 {
		t.Errorf("model_change must apply to later messages, and absent cache fields stay unreported: %+v", b)
	}
}

func TestClineUsesMetadataModelByTimeAndSkipsUnusableRequests(t *testing.T) {
	evs, st := parseFixture(t, vscodeExts[0], "vscode/User/globalStorage/saoudrizwan.claude-dev/tasks/1790000000000/ui_messages.json")
	if len(evs) != 2 || sum(evs) != 650+10 {
		t.Fatalf("events=%d total=%d, want 2 / 660 (in-flight and cancelled requests skipped)", len(evs), sum(evs))
	}
	a, b := find(evs, "claude-sonnet-5-5"), find(evs, "gpt-5.4")
	if a == nil || a.Input != 100 || a.CacheRead != 300 || a.CacheWrite != 200 || a.Output != 50 {
		t.Errorf("first request: %+v", a)
	}
	if b == nil || b.Input != 7 || b.Output != 3 || b.Has&model.HasCacheRead == 0 {
		t.Errorf("second request must use the model active at its time, with reported zero caches: %+v", b)
	}
	if st.Malformed != 1 {
		t.Errorf("malformed=%d (embedded text that is not JSON)", st.Malformed)
	}
}

func TestRooKiloZooReadModelFromHistoryAndSkipMissingUsage(t *testing.T) {
	roo, _ := parseFixture(t, vscodeExts[1], "vscode/User/globalStorage/rooveterinaryinc.roo-cline/tasks/roo-task-1/ui_messages.json")
	if len(roo) != 1 || roo[0].Model != "claude-opus-5-5" || roo[0].Total() != 105 {
		t.Errorf("roo (last <model> in the history wins): %+v", roo)
	}
	kilo, _ := parseFixture(t, vscodeExts[2], "vscode/User/globalStorage/kilocode.kilo-code/tasks/kilo-task-1/ui_messages.json")
	if len(kilo) != 1 || kilo[0].Total() != 25 || kilo[0].Model != "unknown" {
		t.Errorf("kilo (usage_missing skipped; no history = unknown model): %+v", kilo)
	}
	zoo, _ := parseFixture(t, vscodeExts[3], "vscode/User/globalStorage/zoocodeorganization.zoo-code/tasks/zoo-task-1/ui_messages.json")
	if len(zoo) != 1 || zoo[0].Provider != "zoo" || zoo[0].Total() != 2 {
		t.Errorf("zoo: %+v", zoo)
	}
}

func TestDetectionHonoursOverridesAndStaysQuietWhenMissing(t *testing.T) {
	t.Setenv("GEMINI_HOME", t.TempDir())
	t.Setenv("QWEN_HOME", t.TempDir())
	t.Setenv("PI_HOME", t.TempDir())
	t.Setenv("GG_VSCODE_USER_DIRS", t.TempDir())
	for _, p := range []Provider{Gemini{}, Qwen{}, Pi{}, vscodeExts[0]} {
		if d := p.Detect(); d.Status != NotFound || d.Detail == "" || len(p.Files()) != 0 {
			t.Errorf("%s: %+v", p.ID(), d)
		}
	}
	t.Setenv("GEMINI_HOME", filepath.Join("..", "..", "testdata", "gemini"))
	d := Gemini{}.Detect()
	if d.Status != Ready || d.Files != 2 {
		t.Errorf("gemini fixture dir: %+v", d)
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o644) }

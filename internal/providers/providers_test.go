package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

func parse(t *testing.T, p Provider, rel string) ([]model.Event, model.ParseStats) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", rel)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	evs, st, err := ParseAll(p, path, f)
	if err != nil {
		t.Fatal(err)
	}
	return evs, st
}

func byModel(evs []model.Event, m string) *model.Event {
	for i := range evs {
		if evs[i].Model == m {
			return &evs[i]
		}
	}
	return nil
}

func TestClaudeDedupesStreamedChunksAndKeepsLargestCounters(t *testing.T) {
	evs, st := parse(t, Claude{}, "claude/session-a.jsonl")
	if len(evs) != 4 {
		t.Fatalf("want 4 events (msg_1 x3 chunks counted once, synthetic skipped), got %d", len(evs))
	}
	var m1, m2 *model.Event
	for i := range evs {
		switch evs[i].Key {
		case "claude:msg_1":
			m1 = &evs[i]
		case "claude:msg_2":
			m2 = &evs[i]
		}
	}
	if m1 == nil || m1.Input != 2 || m1.CacheWrite != 1000 || m1.CacheWrite1h != 1000 || m1.Output != 50 || m1.Reasoning != 20 {
		t.Fatalf("msg_1 wrong: %+v", m1)
	}
	if m2 == nil || m2.Output != 200 || m2.CacheRead != 1000 {
		t.Fatalf("streamed chunks must keep the final (largest) output: %+v", m2)
	}
	if m1.Project != "/work/alpha" || m1.Session != "sess-a" {
		t.Fatalf("project/session: %+v", m1)
	}
	if st.Malformed != 3 {
		t.Errorf("malformed: want 3 (bad json, bad timestamp, wrong type), got %d", st.Malformed)
	}
	if st.Anomalies != 2 {
		t.Errorf("anomalies: want 2 (negative and absurd counters), got %d", st.Anomalies)
	}
}

func TestClaudeNeverPresentsMissingFieldsAsReported(t *testing.T) {
	evs, _ := parse(t, Claude{}, "claude/session-a.jsonl")
	e := byModel(evs, "mystery-model-9")
	if e == nil {
		t.Fatal("unknown model must still be recorded")
	}
	if e.Has != 0 {
		t.Errorf("no cache/reasoning fields in source: Has must be 0, got %b", e.Has)
	}
}

func TestClaudeClampsHostileCounters(t *testing.T) {
	evs, _ := parse(t, Claude{}, "claude/session-a.jsonl")
	e := byModel(evs, "claude-opus-5-5")
	if e == nil || e.Input != 0 || e.CacheRead != 0 || e.Output != 7 {
		t.Fatalf("negative/absurd counters must become 0 without touching others: %+v", e)
	}
}

// One API call written as two lines, only one of them carrying requestId, is
// still one call: the key is the message id alone.
func TestClaudeSameMessageIDWithAndWithoutRequestIDCountsOnce(t *testing.T) {
	usage := `"usage":{"input_tokens":2,"output_tokens":%d}`
	lines := []string{
		`{"type":"assistant","sessionId":"s","requestId":"r1","timestamp":"2026-10-03T00:00:00Z","message":{"id":"m1","model":"claude-opus-5-5",` + fmt.Sprintf(usage, 50) + `}}`,
		`{"type":"assistant","sessionId":"s","timestamp":"2026-10-03T00:00:01Z","message":{"id":"m1","model":"claude-opus-5-5",` + fmt.Sprintf(usage, 60) + `}}`,
	}
	evs, _, err := ParseAll(Claude{}, "/x/s.jsonl", strings.NewReader(strings.Join(lines, "\n")+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Input != 2 || evs[0].Output != 60 {
		t.Fatalf("one call must be one event with the largest counters, got %+v", evs)
	}
}

func TestCodexRecordsPreferredAndRebroadcastsIgnored(t *testing.T) {
	evs, st := parse(t, Codex{}, "codex/records.jsonl")
	if len(evs) != 4 {
		t.Fatalf("want 4 events (A, B once, C, E), got %d", len(evs))
	}
	var total int64
	for _, e := range evs {
		total += e.Total()
	}
	if total != 1100+1260+520+5 {
		t.Errorf("total tokens = %d", total)
	}
	a := byModel(evs, "gpt-5.4")
	if a == nil {
		t.Fatal("model from turn_context missing")
	}
	var A *model.Event
	for i := range evs {
		if evs[i].Key == "codex:resp:resp_A" {
			A = &evs[i]
		}
	}
	// OpenAI input includes cached tokens: Input must be the uncached part.
	if A.Input != 200 || A.CacheRead != 800 || A.Output != 100 || A.Reasoning != 40 || A.Project != "/work/gamma" {
		t.Errorf("resp_A normalisation wrong: %+v", A)
	}
	if byModel(evs, "gpt-6-sol") == nil {
		t.Error("model switch mid-session not tracked")
	}
	if st.Malformed != 3 || st.Anomalies != 1 {
		t.Errorf("malformed=%d (want 3: empty id, no usage, truncated line) anomalies=%d (want 1)", st.Malformed, st.Anomalies)
	}
}

func TestCodexLegacyCumulativeCountersUseDeltas(t *testing.T) {
	evs, _ := parse(t, Codex{}, "codex/legacy.jsonl")
	if len(evs) != 3 {
		t.Fatalf("want 3 events (re-broadcast skipped), got %d: %+v", len(evs), evs)
	}
	var total, in, cr int64
	for _, e := range evs {
		total += e.Total()
		in += e.Input
		cr += e.CacheRead
	}
	// 110 (first) + 230 (delta 110->340) + 55 (counter reset, uses last_token_usage)
	if total != 395 || in != 40+60+50 || cr != 60+140 {
		t.Errorf("total=%d in=%d cr=%d", total, in, cr)
	}
}

func TestCodexResumeEqualsFullParse(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "codex", "legacy.jsonl")
	b, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(b), "\n")
	for cut := 1; cut < len(lines); cut++ {
		p1 := Codex{}.NewParser(path, nil)
		var st model.ParseStats
		_, _ = ReadLines(strings.NewReader(strings.Join(lines[:cut], "")), &st, true, p1.Line)
		ev1, st1 := p1.Result()
		// copy as the cache would
		res := &Resume{Events: append([]model.Event(nil), ev1...), Stats: st1, State: p1.State()}
		p2 := Codex{}.NewParser(path, res)
		_, _ = ReadLines(strings.NewReader(strings.Join(lines[cut:], "")), &st, true, p2.Line)
		got, _ := p2.Result()
		want, _ := parse(t, Codex{}, "codex/legacy.jsonl")
		if len(got) != len(want) {
			t.Fatalf("cut %d: resumed %d events, full %d", cut, len(got), len(want))
		}
		for i := range got {
			if got[i].Total() != want[i].Total() {
				t.Fatalf("cut %d event %d differs", cut, i)
			}
		}
	}
}

func TestReadLinesLeavesPartialTailUnlessAllowed(t *testing.T) {
	var st model.ParseStats
	var n int
	c, _ := ReadLines(strings.NewReader("a\nb\npartial"), &st, false, func([]byte) { n++ })
	if n != 2 || c != 4 {
		t.Errorf("partial tail must be left for the next run: lines=%d consumed=%d", n, c)
	}
	n = 0
	c, _ = ReadLines(strings.NewReader("a\nb\nfinal"), &st, true, func([]byte) { n++ })
	if n != 3 || c != 9 {
		t.Errorf("idle file: tail is a real record: lines=%d consumed=%d", n, c)
	}
}

func TestReadLinesSkipsOversizedLinesAndCountsThem(t *testing.T) {
	big := strings.Repeat("x", maxLine+10)
	var st model.ParseStats
	var got []string
	_, err := ReadLines(strings.NewReader("ok1\n"+big+"\nok2\n"), &st, true, func(l []byte) { got = append(got, string(l)) })
	if err != nil || len(got) != 2 || got[0] != "ok1" || got[1] != "ok2" || st.Oversized != 1 {
		t.Errorf("got=%v oversized=%d err=%v", got, st.Oversized, err)
	}
}

func FuzzParsersNeverPanic(f *testing.F) {
	for _, s := range []string{`{"type":"assistant","usage":1}`, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1e999}}}`, `[]`, `null`, "\x00\xff", `{"payload":{"info":{"total_token_usage":null}}} token_count`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, p := range []Provider{Claude{}, Codex{}, Gemini{}, Qwen{}, Pi{}, opencodeFamily[0], opencodeFamily[1],
			vscodeExts[0], vscodeExts[1], vscodeExts[2], vscodeExts[3]} {
			if _, _, err := ParseAll(p, "x.jsonl", strings.NewReader(s+"\n"+s)); err != nil {
				t.Fatal(err)
			}
			if wf, ok := p.(WholeFiler); ok && wf.WholeFile("x.json") { // single-document parsers get arbitrary bytes too
				ps := p.NewParser("x.json", nil)
				ps.Line([]byte(s))
				ps.Result()
			}
		}
	})
}

// A session that started on an older Codex (cumulative token_count events)
// and continued on a newer one (exact records) keeps both: the old usage is
// not thrown away, and token_count events after the first record (which only
// repeat what the records say) are not counted twice.
func TestCodexMixedLegacyThenRecords(t *testing.T) {
	cnt := func(ts string, in, out int) string {
		return fmt.Sprintf(`{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}}`, ts, in, out, in+out)
	}
	rec := func(ts, id string, in, out int) string {
		return fmt.Sprintf(`{"timestamp":"%s","type":"token_usage_record","payload":{"response_id":"%s","usage":{"input_tokens":%d,"output_tokens":%d}}}`, ts, id, in, out)
	}
	lines := []string{
		`{"timestamp":"2026-10-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-5.4","cwd":"/w/a"}}`,
		cnt("2026-10-01T10:00:01Z", 100, 50), cnt("2026-10-01T10:00:02Z", 300, 100),
		rec("2026-10-01T10:01:00Z", "r1", 10, 5),
		cnt("2026-10-01T10:01:01Z", 310, 105), // repeats r1: must not be counted
		rec("2026-10-01T10:02:00Z", "r2", 20, 8),
	}
	full := strings.Join(lines, "\n") + "\n"
	evs, _, err := ParseAll(Codex{}, "/x/s.jsonl", strings.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for i := range evs {
		total += evs[i].Total()
	}
	if len(evs) != 4 || total != 400+(10+5)+(20+8) {
		t.Fatalf("want 2 legacy deltas (400 tokens) + 2 records (43), got %d events totalling %d: %+v", len(evs), total, evs)
	}
	// Resuming from any line boundary gives the same events.
	for cut := 1; cut < len(lines); cut++ {
		p1 := Codex{}.NewParser("/x/s.jsonl", nil)
		for _, l := range lines[:cut] {
			p1.Line([]byte(l))
		}
		e1, st1 := p1.Result()
		p2 := Codex{}.NewParser("/x/s.jsonl", &Resume{Events: append([]model.Event(nil), e1...), Stats: st1, State: p1.State()})
		for _, l := range lines[cut:] {
			p2.Line([]byte(l))
		}
		e2, _ := p2.Result()
		if !reflect.DeepEqual(e2, evs) {
			t.Fatalf("resume at %d differs:\n%+v\n%+v", cut, e2, evs)
		}
	}
}

// The saved resume state must not duplicate the events.
func TestCodexStateDoesNotCarryEvents(t *testing.T) {
	p := Codex{}.NewParser("/x/s.jsonl", nil)
	for i := 0; i < 200; i++ {
		p.Line([]byte(fmt.Sprintf(`{"timestamp":"2026-10-01T10:00:00Z","type":"token_usage_record","payload":{"response_id":"r%d","usage":{"input_tokens":1,"output_tokens":1}}}`, i)))
		p.Line([]byte(fmt.Sprintf(`{"timestamp":"2026-10-01T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}}`, i*2+2, i*2+2, i*4+4)))
	}
	if n := len(p.State()); n > 400 {
		t.Errorf("resume state is %d bytes: it must stay small and independent of the number of events", n)
	}
}

// Without a message id a record cannot be told apart from the other blocks of
// its reply, so it is malformed, whatever else it carries (a uuid included).
func TestClaudeRecordWithoutMessageIDIsMalformed(t *testing.T) {
	usage := `"usage":{"input_tokens":2,"output_tokens":5}`
	cases := []struct {
		name string
		line string
	}{
		{"no id", `{"type":"assistant","sessionId":"s","timestamp":"2026-10-03T00:00:00Z","message":{"model":"claude-opus-5-5",` + usage + `}}`},
		{"no id, uuid", `{"type":"assistant","uuid":"u1","sessionId":"s","timestamp":"2026-10-03T00:00:00Z","message":{"model":"claude-opus-5-5",` + usage + `}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, st, err := ParseAll(Claude{}, "/x/s.jsonl", strings.NewReader(c.line+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 0 || st.Malformed != 1 {
				t.Fatalf("want no event and 1 malformed, got %d events, malformed=%d", len(evs), st.Malformed)
			}
		})
	}
}

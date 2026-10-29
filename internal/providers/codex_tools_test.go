package providers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Codex writes each tool call before the usage record of the response that
// made it. A call belongs to the next usage event; a resumed parse gives the
// same events as a whole one, at every line boundary.
func TestCodexToolCallsBelongToTheNextUsageRecord(t *testing.T) {
	big := strings.Repeat("x", 50000) // a patch: the input is skipped, not read
	lines := []string{
		`{"timestamp":"2026-10-03T00:00:00Z","type":"turn_context","payload":{"model":"gpt-5.4","cwd":"/w/a"}}`,
		`{"timestamp":"2026-10-03T00:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","status":"completed","call_id":"c1","name":"apply_patch","input":"` + big + `"}}`,
		`{"timestamp":"2026-10-03T00:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls\"}","call_id":"c2"}}`,
		`{"timestamp":"2026-10-03T00:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c2","output":"ok"}}`,
		`{"timestamp":"2026-10-03T00:00:04Z","type":"token_usage_record","payload":{"response_id":"r1","usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"timestamp":"2026-10-03T00:00:05Z","type":"response_item","payload":{"type":"web_search_call","status":"completed"}}`,
		`{"timestamp":"2026-10-03T00:00:06Z","type":"token_usage_record","payload":{"response_id":"r2","usage":{"input_tokens":20,"output_tokens":8}}}`,
		`{"timestamp":"2026-10-03T00:00:07Z","type":"token_usage_record","payload":{"response_id":"r2","usage":{"input_tokens":20,"output_tokens":8}}}`,
		`{"timestamp":"2026-10-03T00:00:08Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c3"}}`,
		`{"timestamp":"2026-10-03T00:00:09Z","type":"token_usage_record","payload":{"response_id":"r3","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	full := strings.Join(lines, "\n") + "\n"
	evs, _, err := ParseAll(Codex{}, "/x/s.jsonl", strings.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"c1=apply_patch,c2=exec_command", "ws:2026-10-03T00:00:05Z=web_search", "c3=exec_command"}
	if len(evs) != 3 {
		t.Fatalf("three replies, got %d", len(evs))
	}
	for i, e := range evs {
		if e.Tools != want[i] {
			t.Errorf("reply %d tools = %q, want %q", i, e.Tools, want[i])
		}
	}
	// the calls of r3 are not lost by the duplicate record, and names come out clean
	if got := model.ToolNames(evs[0].Tools); strings.Join(got, ",") != "apply_patch,exec_command" {
		t.Errorf("names: %v", got)
	}
	// resuming at every line boundary gives the same events
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
			t.Fatalf("resume at line %d differs:\n%+v\n%+v", cut, e2, evs)
		}
	}
}

// In files of the older format, the calls come before the cumulative count
// that reports their response.
func TestCodexToolCallsInLegacyCounts(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-5.4","cwd":"/w/a"}}`,
		`{"timestamp":"2026-10-01T10:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"k1"}}`,
		`{"timestamp":"2026-10-01T10:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150}}}}`,
		`{"timestamp":"2026-10-01T10:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"output_tokens":100,"total_tokens":400}}}}`,
	}
	evs, _, err := ParseAll(Codex{}, "/x/s.jsonl", strings.NewReader(strings.Join(lines, "\n")+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Tools != "k1=exec_command" || evs[1].Tools != "" {
		t.Errorf("the call goes with the first count it precedes: %+v", evs)
	}
}

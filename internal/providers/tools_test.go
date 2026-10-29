package providers

import (
	"strings"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Tool uses are read from the assistant message they belong to; a skill is
// named after the skill; streamed chunks of one message keep the complete list.
func TestClaudeToolUsesPerMessage(t *testing.T) {
	msg := func(id, content string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"r-` + id + `","timestamp":"2026-10-03T00:00:00Z","message":{"id":"` + id + `","model":"claude-opus-5-5","content":[` + content + `],"usage":{"input_tokens":5,"output_tokens":5}}}`
	}
	bash := `{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}`
	read := `{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"x"}}`
	skill := `{"type":"tool_use","id":"t3","name":"Skill","input":{"skill":"review"}}`
	mcp := `{"type":"tool_use","id":"t4","name":"mcp__docs__guide","input":{}}`
	text := `{"type":"text","text":"hello"}`
	lines := []string{
		msg("m1", text+","+bash),                    // first chunk: one tool so far
		msg("m1", text+","+bash+","+read+","+skill), // the same message, complete
		msg("m2", mcp),
	}
	evs, _, err := ParseAll(Claude{}, "/x/s.jsonl", strings.NewReader(strings.Join(lines, "\n")+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("two messages, got %d", len(evs))
	}
	if !anyEvent(evs, "Bash,Read,skill:review") {
		t.Errorf("the complete list of the streamed message: %+v", evs)
	}
	if !anyEvent(evs, "mcp__docs__guide") {
		t.Errorf("an MCP tool keeps its full name: %+v", evs)
	}
	if n := len(model.ToolNames("a,b,c")); n != 3 {
		t.Errorf("ToolNames: %d", n)
	}
}

func anyEvent(evs []model.Event, tools string) bool {
	for _, e := range evs {
		if e.Tools == tools {
			return true
		}
	}
	return false
}

// A tool name from a log is cleaned and bounded like every other log string.
func TestToolNamesAreCleanedAndBounded(t *testing.T) {
	got := model.ToolList([]string{"Bash\x1b]0;x\x07", strings.Repeat("n", 500), "a,b"})
	names := model.ToolNames(got)
	if len(names) != 3 || strings.ContainsRune(names[0], 0x1b) || len(names[1]) > model.MaxToolName || names[2] != "a;b" {
		t.Errorf("names: %q", names)
	}
}

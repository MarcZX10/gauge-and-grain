package providers

import (
	"strings"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// Claude writes one log line per content block of a reply, all with the same
// message id and usage. The tool uses of the reply are the union of those
// lines, each use counted once, a skill is named after the skill, and an MCP
// tool keeps its full name.
func TestClaudeToolUsesAreUnionedAcrossLinesOfOneReply(t *testing.T) {
	line := func(id, block string) string {
		return `{"type":"assistant","sessionId":"s","requestId":"r1","timestamp":"2026-10-03T00:00:00Z","message":{"id":"` + id + `","model":"claude-opus-5-5","content":[` + block + `],"usage":{"input_tokens":5,"output_tokens":5}}}`
	}
	bash := `{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}`
	read := `{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"x"}}`
	skill := `{"type":"tool_use","id":"t3","name":"Skill","input":{"skill":"review"}}`
	mcp := `{"type":"tool_use","id":"t4","name":"mcp__docs__guide","input":{}}`
	text := `{"type":"text","text":"hello"}`
	lines := []string{
		line("m1", text),
		line("m1", bash),
		line("m1", read),
		line("m1", skill),
		line("m1", bash), // a repeated line of the same reply
		line("m2", mcp),
	}
	evs, _, err := ParseAll(Claude{}, "/x/s.jsonl", strings.NewReader(strings.Join(lines, "\n")+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("two replies, got %d", len(evs))
	}
	var m1, m2 model.Event
	for _, e := range evs {
		if e.Tools == "" || strings.Contains(e.Tools, "t4=") {
			m2 = e
		} else {
			m1 = e
		}
	}
	if got := model.ToolNames(m1.Tools); strings.Join(got, ",") != "Bash,Read,skill:review" {
		t.Errorf("reply 1 tools = %v (raw %q): every block once, from all its lines", got, m1.Tools)
	}
	if got := model.ToolNames(m2.Tools); len(got) != 1 || got[0] != "mcp__docs__guide" {
		t.Errorf("reply 2 tools = %v", got)
	}
}

// The text of a tool name from a log is cleaned and bounded, and separators
// inside a name cannot split the list.
func TestToolEntriesAreCleanedAndBounded(t *testing.T) {
	e := model.ToolEntry("id,1", "Bash\x1b]0;x\x07,a=b")
	if strings.ContainsRune(e, 0x1b) || strings.Count(e, ",") != 0 {
		t.Errorf("entry %q", e)
	}
	long := model.ToolEntry("x", strings.Repeat("n", 500))
	if got := model.ToolNames(long); len(got[0]) > model.MaxToolName {
		t.Errorf("a long name must be bounded: %d bytes", len(got[0]))
	}
}

// Merging two copies of a call keeps every distinct use once.
func TestMergeToolsUnionsByID(t *testing.T) {
	a := model.ToolEntry("t1", "Bash") + "," + model.ToolEntry("t2", "Read")
	b := model.ToolEntry("t2", "Read") + "," + model.ToolEntry("t3", "Edit")
	ev := model.Event{Tools: a}
	ev.Merge(&model.Event{Tools: b})
	if got := model.ToolNames(ev.Tools); strings.Join(got, ",") != "Bash,Read,Edit" {
		t.Errorf("union = %v", got)
	}
}

// A wrong-typed field among the content or the names of a record (a tool input
// that is a string, a cwd that is a number) does not drop its valid usage.
func TestClaudeBadUnrelatedFieldKeepsUsage(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s","cwd":42,"timestamp":"2026-10-03T00:00:00Z","message":{"id":"m9","model":"claude-opus-5-5",` +
		`"content":[{"type":"tool_use","id":"t1","name":"Bash","input":"ls -la"},{"type":"tool_use","id":"t2","name":"Skill","input":{"skill":"review"}}],` +
		`"usage":{"input_tokens":3,"output_tokens":4}}}`
	evs, st, err := ParseAll(Claude{}, "/x/s.jsonl", strings.NewReader(line+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || st.Malformed != 0 {
		t.Fatalf("want one event and no malformed line, got %d events, malformed=%d", len(evs), st.Malformed)
	}
	e := evs[0]
	if e.Input != 3 || e.Output != 4 || e.Session != "s" || e.Project != "" {
		t.Errorf("usage or names: %+v", e)
	}
	if e.Tools != "t1=Bash,t2=skill:review" {
		t.Errorf("tools = %q: the block with a string input keeps its name", e.Tools)
	}
}

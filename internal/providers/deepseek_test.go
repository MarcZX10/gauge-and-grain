package providers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func writeZstd(t *testing.T, path, content string) {
	t.Helper()
	var b bytes.Buffer
	w, err := zstd.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(content))
	w.Close()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Synthetic session built from the documented event format.
const dshSession = `{"type":"session","id":"ds-1","cwd":"/work/ds","seq":1}
{"type":"user/message","seq":2,"time":1790300000000,"data":{"id":"u1","source":{"kind":"user"},"content":"hi"}}
{"type":"assistant/message","seq":3,"time":1790300001000,"data":{"turn":1,"step":1,"message":{"id":"a1","source":{"kind":"model","model":"deepseek-v4-flash"}},"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":4,"reasoningTokens":5}}}
{"type":"assistant/message","seq":3,"time":1790300001000,"data":{"turn":1,"step":1,"message":{"id":"a1","source":{"kind":"model","model":"deepseek-v4-flash"}},"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":4,"reasoningTokens":5}}}
{"type":"assistant/message","seq":4,"time":1790300002000,"data":{"message":{"id":"a2","source":{"kind":"model","model":"deepseek-v4-flash"}}}}
{"type":"assistant/message","seq":5,"time":1790300003000,"data":{"message":{"id":"a3"},"usage":{"inputTokens":10,"outputTokens":2}}}
{ not json
`

// An anomalous counter is one anomaly, not one per use of the value.
func TestDeepSeekNegativeOutputIsCountedOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws", "ds-2", "session.jsonl.zstd")
	writeZstd(t, p, `{"type":"assistant/message","seq":1,"time":1790300000000,"data":{"message":{"id":"n1","source":{"kind":"model","model":"deepseek-v4-flash"}},"usage":{"inputTokens":1,"outputTokens":-7,"reasoningTokens":3}}}`+"\n")
	evs, st, err := DeepSeek{}.ParsePath(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Output != 0 || evs[0].Reasoning != 0 || st.Anomalies != 1 {
		t.Fatalf("output clamped to 0 and counted once: events=%+v anomalies=%d", evs, st.Anomalies)
	}
}

func TestDeepSeekZstdSession(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws", "ds-1", "session.jsonl.zstd")
	writeZstd(t, p, dshSession)
	evs, st, err := DeepSeek{}.ParsePath(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || sum(evs) != 154+12 {
		t.Fatalf("events=%d total=%d, want 2 / 166 (duplicate and usage-less messages ignored)", len(evs), sum(evs))
	}
	a1 := find(evs, "deepseek-v4-flash")
	if a1.Input != 100 || a1.CacheRead != 30 || a1.CacheWrite != 4 || a1.Output != 20 || a1.Reasoning != 5 || a1.Project != "/work/ds" || a1.Session != "ds-1" {
		t.Errorf("a1: %+v", a1)
	}
	if a3 := find(evs, "unknown"); a3 == nil || a3.Total() != 12 {
		t.Errorf("a model-less message is kept with an unknown model: %+v", a3)
	}
	if st.Malformed != 1 {
		t.Errorf("malformed=%d", st.Malformed)
	}
}

func TestDeepSeekForkedSessionCountsInheritedMessagesOnce(t *testing.T) {
	// the child repeats the parent's message a1 (same id) and adds its own
	child := `{"type":"session","id":"ds-2","cwd":"/work/ds","parentSession":"ds-1","seedLength":4}
{"type":"assistant/message","seq":1,"time":1790300001000,"data":{"message":{"id":"a1","source":{"kind":"model","model":"deepseek-v4-flash"}},"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":4,"reasoningTokens":5}}}
{"type":"assistant/message","seq":9,"time":1790300009000,"data":{"message":{"id":"b1","source":{"kind":"model","model":"deepseek-v4-flash"}},"usage":{"inputTokens":1,"outputTokens":1}}}
`
	dir := t.TempDir()
	t.Setenv("DSH_HOME", dir)
	writeZstd(t, filepath.Join(dir, "sessions", "ws", "ds-1", "session.jsonl.zstd"), dshSession)
	writeZstd(t, filepath.Join(dir, "sessions", "ws", "ds-2", "session.v4.jsonl.zstd"), child)
	if d := (DeepSeek{}).Detect(); d.Status != Ready || d.Files != 2 {
		t.Fatalf("%+v", d)
	}
	// each file parses on its own; de-duplication across files is the scanner's job
	var keys = map[string]int{}
	for _, f := range (DeepSeek{}).Files() {
		evs, _, err := DeepSeek{}.ParsePath(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			keys[e.Key]++
		}
	}
	if keys["deepseek:a1"] != 2 || keys["deepseek:b1"] != 1 {
		t.Errorf("keys %v", keys)
	}
}

func TestDeepSeekGarbageIsAnErrorNotACrash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws", "x", "session.jsonl.zstd")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("not zstd at all"), 0o644)
	if _, _, err := (DeepSeek{}).ParsePath(context.Background(), p); err == nil {
		t.Error("a file that is not zstd must be reported as unreadable")
	}
}

func zstdBytes(t *testing.T, plain []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := zstd.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(plain)
	w.Close()
	return b.Bytes()
}

const dshLine = `{"type":"assistant/message","time":1790100000000,"data":{"message":{"id":"m%d","source":{"model":"deepseek-v4"}},"usage":{"inputTokens":10,"outputTokens":5}}}` + "\n"

// A session still being written ends in the middle of a zstd frame: what
// decoded so far counts, and the file is not an error.
func TestDeepSeekTruncatedArchiveKeepsWhatDecoded(t *testing.T) {
	var plain bytes.Buffer
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&plain, dshLine, i)
	}
	z := zstdBytes(t, plain.Bytes())
	p := filepath.Join(t.TempDir(), "session.jsonl.zstd")
	if err := os.WriteFile(p, z[:len(z)*2/3], 0o644); err != nil {
		t.Fatal(err)
	}
	evs, _, err := DeepSeek{}.ParsePath(context.Background(), p)
	if err != nil || len(evs) == 0 || len(evs) >= 2000 {
		t.Fatalf("want some but not all events and no error, got %d, %v", len(evs), err)
	}
}

// A small archive that expands without bound stops at the limit and is flagged.
func TestDeepSeekDecompressionBomb(t *testing.T) {
	old := maxDecompressed
	maxDecompressed = 4 << 20
	defer func() { maxDecompressed = old }()
	var plain bytes.Buffer
	fmt.Fprintf(&plain, dshLine, 1)
	plain.Write(bytes.Repeat([]byte("\n"), 32<<20)) // blank lines: compress to almost nothing
	z := zstdBytes(t, plain.Bytes())
	if len(z) > 1<<20 {
		t.Fatalf("test archive too big: %d", len(z))
	}
	p := filepath.Join(t.TempDir(), "session.jsonl.zstd")
	if err := os.WriteFile(p, z, 0o644); err != nil {
		t.Fatal(err)
	}
	evs, st, err := DeepSeek{}.ParsePath(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Oversized == 0 || len(evs) != 1 {
		t.Errorf("exceeding the decompression limit must be counted (oversized=%d) and keep what was read (%d events)", st.Oversized, len(evs))
	}
}

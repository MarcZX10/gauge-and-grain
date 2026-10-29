// demodata writes a synthetic, reproducible AI-usage history for screenshots,
// demos and manual testing. Nothing here comes from a real person's logs.
//
//	go run ./scripts/demodata -out /tmp/gg-demo
//	eval "$(go run ./scripts/demodata -out /tmp/gg-demo -env)"   # point gg at it
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	out  = flag.String("out", "gg-demo", "directory to write the demo history into")
	days = flag.Int("days", 75, "how many days of history, ending today")
	seed = flag.Int64("seed", 20261029, "random seed (same seed, same data)")
	env  = flag.Bool("env", false, "print shell exports that point gg at -out, and exit")
)

func main() {
	flag.Parse()
	abs, _ := filepath.Abs(*out)
	if *env {
		for k, v := range map[string]string{
			"CLAUDE_CONFIG_DIR": "claude", "CODEX_HOME": "codex", "GEMINI_HOME": "gemini", "OPENCODE_HOME": "opencode",
			"QWEN_HOME": "none/qwen", "PI_HOME": "none/pi", "KILO_HOME": "none/kilo", "DSH_HOME": "none/dsh", "GG_VSCODE_USER_DIRS": "none/vscode",
			"GG_CACHE_DIR": "cache", "GG_CONFIG_DIR": "config",
		} {
			fmt.Printf("export %s=%q\n", k, filepath.Join(abs, v))
		}
		return
	}
	g := &gen{rnd: rand.New(rand.NewSource(*seed)), root: abs}
	if err := os.RemoveAll(abs); err != nil {
		fatal(err)
	}
	today := time.Now()
	for d := *days - 1; d >= 0; d-- {
		day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location()).AddDate(0, 0, -d)
		g.day(day, d)
	}
	fmt.Printf("wrote %d sessions to %s\n", g.sessions, abs)
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "demodata:", err); os.Exit(1) }

type gen struct {
	rnd      *rand.Rand
	root     string
	sessions int
	n        int
}

var projects = []string{"webshop", "api-gateway", "docs-site", "mobile-app", "infra", "data-pipeline"}

func (g *gen) id(prefix string) string {
	g.n++
	h := sha256.Sum256([]byte(fmt.Sprint(prefix, g.n, g.rnd.Int63())))
	return prefix + hex.EncodeToString(h[:])[:20]
}

func (g *gen) write(path string, b []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fatal(err)
	}
}

// intensity shapes the history: slow growth, quieter weekends, a few spikes.
func (g *gen) intensity(day time.Time, ago int) float64 {
	v := 0.55 + 0.9*float64(*days-ago)/float64(*days)
	if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
		v *= 0.3
	}
	if ago%17 == 5 {
		v *= 2.1 // a release crunch
	}
	return v * (0.75 + 0.5*g.rnd.Float64())
}

func (g *gen) day(day time.Time, ago int) {
	k := g.intensity(day, ago)
	for i, n := 0, int(math.Round(k*2.2)); i < n; i++ { // Claude Code sessions
		g.claude(day.Add(time.Duration(8*3600+g.rnd.Intn(10*3600)) * time.Second))
	}
	for i, n := 0, int(math.Round(k*1.1)); i < n; i++ {
		g.codex(day.Add(time.Duration(9*3600+g.rnd.Intn(9*3600)) * time.Second))
	}
	if g.rnd.Float64() < 0.45*k {
		g.gemini(day.Add(time.Duration(10*3600+g.rnd.Intn(8*3600)) * time.Second))
	}
	if g.rnd.Float64() < 0.35*k {
		g.opencode(day.Add(time.Duration(10*3600+g.rnd.Intn(8*3600)) * time.Second))
	}
}

func (g *gen) pickProject() string {
	return projects[int(math.Pow(g.rnd.Float64(), 1.7)*float64(len(projects)))]
}

func (g *gen) claude(start time.Time) {
	g.sessions++
	proj := g.pickProject()
	cwd := "/home/dev/" + proj
	sid := g.id("")
	model := pick(g.rnd, []string{"claude-sonnet-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5"})
	var sb strings.Builder
	t := start
	cache := 8000 + g.rnd.Intn(20000)
	for c, calls := 0, 12+g.rnd.Intn(70); c < calls; c++ {
		t = t.Add(time.Duration(8+g.rnd.Intn(110)) * time.Second)
		cache += 400 + g.rnd.Intn(2500)
		in, write := 2+g.rnd.Intn(6), 0
		if g.rnd.Float64() < 0.2 {
			write = 1500 + g.rnd.Intn(14000)
		}
		usage := map[string]any{"input_tokens": in, "cache_read_input_tokens": cache, "cache_creation_input_tokens": write, "output_tokens": 80 + g.rnd.Intn(2400)}
		if g.rnd.Float64() < 0.5 {
			usage["output_tokens_details"] = map[string]any{"thinking_tokens": g.rnd.Intn(600)}
		}
		rec := map[string]any{"type": "assistant", "sessionId": sid, "cwd": cwd, "requestId": g.id("req_"), "timestamp": t.UTC().Format(time.RFC3339Nano),
			"message": map[string]any{"id": g.id("msg_"), "model": model, "usage": usage}}
		line, _ := json.Marshal(rec)
		sb.Write(line)
		sb.WriteByte('\n')
		if g.rnd.Float64() < 0.5 { // Claude Code repeats a response once per content block
			sb.Write(line)
			sb.WriteByte('\n')
		}
	}
	g.write(filepath.Join(g.root, "claude", "projects", "-home-dev-"+proj, sid+".jsonl"), []byte(sb.String()))
}

func (g *gen) codex(start time.Time) {
	g.sessions++
	proj := g.pickProject()
	sid := g.id("")
	model := pick(g.rnd, []string{"gpt-6-sol", "gpt-6-sol", "gpt-5.4", "gpt-6-luna", "codex-auto-review"})
	var sb strings.Builder
	ts := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	emit := func(v map[string]any) {
		b, _ := json.Marshal(v)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	emit(map[string]any{"timestamp": ts(start), "type": "session_meta", "payload": map[string]any{"session_id": sid, "id": sid, "cwd": "/home/dev/" + proj}})
	emit(map[string]any{"timestamp": ts(start), "type": "turn_context", "payload": map[string]any{"model": model, "cwd": "/home/dev/" + proj}})
	t := start
	ctx := 6000 + g.rnd.Intn(8000)
	for c, calls := 0, 10+g.rnd.Intn(60); c < calls; c++ {
		t = t.Add(time.Duration(6+g.rnd.Intn(90)) * time.Second)
		ctx += 300 + g.rnd.Intn(2200)
		cached := ctx * (55 + g.rnd.Intn(40)) / 100
		out := 60 + g.rnd.Intn(1800)
		emit(map[string]any{"timestamp": ts(t), "type": "token_usage_record", "payload": map[string]any{"session_id": sid, "response_id": g.id("resp_"),
			"usage": map[string]any{"input_tokens": ctx, "cached_input_tokens": cached, "cache_write_input_tokens": 0, "output_tokens": out, "reasoning_output_tokens": out * g.rnd.Intn(40) / 100}}})
	}
	d := start.Local().Format("2006/01/02")
	g.write(filepath.Join(g.root, "codex", "sessions", d, "rollout-"+start.Format("2006-01-02T15-04-05")+"-"+sid+".jsonl"), []byte(sb.String()))
}

func (g *gen) gemini(start time.Time) {
	g.sessions++
	proj := g.id("")[:16]
	sid := g.id("")
	model := pick(g.rnd, []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash"})
	var msgs []map[string]any
	t := start
	for c, calls := 0, 6+g.rnd.Intn(30); c < calls; c++ {
		t = t.Add(time.Duration(10+g.rnd.Intn(100)) * time.Second)
		in := 3000 + g.rnd.Intn(40000)
		msgs = append(msgs, map[string]any{"id": g.id("m"), "timestamp": t.UTC().Format("2006-01-02T15:04:05.000Z"), "type": "gemini", "model": model,
			"tokens": map[string]any{"input": in, "output": 80 + g.rnd.Intn(900), "cached": in * g.rnd.Intn(70) / 100, "thoughts": g.rnd.Intn(500), "tool": 0}})
	}
	doc, _ := json.Marshal(map[string]any{"sessionId": sid, "projectHash": proj, "startTime": start.UTC().Format(time.RFC3339), "messages": msgs})
	g.write(filepath.Join(g.root, "gemini", "tmp", proj, "chats", "session-"+start.Format("2006-01-02T15-04")+".json"), doc)
}

func (g *gen) opencode(start time.Time) {
	g.sessions++
	sid := "ses_" + g.id("")
	proj := "/home/dev/" + g.pickProject()
	model := pick(g.rnd, []string{"claude-sonnet-5-5", "gpt-5.4", "qwen3-coder-plus"})
	t := start
	for c, calls := 0, 5+g.rnd.Intn(25); c < calls; c++ {
		t = t.Add(time.Duration(15+g.rnd.Intn(120)) * time.Second)
		id := "msg_" + g.id("")
		doc, _ := json.Marshal(map[string]any{"id": id, "sessionID": sid, "role": "assistant", "modelID": model, "time": map[string]any{"created": t.UnixMilli()},
			"path": map[string]any{"cwd": proj, "root": proj},
			"tokens": map[string]any{"input": 50 + g.rnd.Intn(400), "output": 100 + g.rnd.Intn(1500), "reasoning": g.rnd.Intn(300),
				"cache": map[string]any{"read": 2000 + g.rnd.Intn(60000), "write": g.rnd.Intn(4000)}}})
		g.write(filepath.Join(g.root, "opencode", "storage", "message", sid, id+".json"), doc)
	}
}

func pick(r *rand.Rand, s []string) string { return s[r.Intn(len(s))] }

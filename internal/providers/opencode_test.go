package providers

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/MarcZX10/gauge-and-grain/internal/model"
)

// makeOpenCodeDB builds a database with the schema OpenCode uses
// (project, session, message, part). It is synthetic, not a real install.
func makeOpenCodeDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE project(id TEXT PRIMARY KEY, worktree TEXT)`,
		`CREATE TABLE session(id TEXT PRIMARY KEY, project_id TEXT, title TEXT)`,
		`CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT, data TEXT)`,
		`INSERT INTO project VALUES ('p1','/work/oc')`,
		`INSERT INTO session VALUES ('s1','p1','a session')`,
		`INSERT INTO message VALUES ('m1','s1',1790100000000,'{"role":"user"}')`,
		`INSERT INTO message VALUES ('m2','s1',1790100001000,'{"role":"assistant","modelID":"claude-sonnet-5-5","time":{"created":1790100001000},"tokens":{"input":10,"output":200,"reasoning":50,"cache":{"read":1000,"write":100}}}')`,
		`INSERT INTO message VALUES ('m3','s1',1790100100000,'{"role":"assistant","model":{"providerID":"openai","modelID":"gpt-5.4"},"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO part VALUES ('pt1','m3','{"type":"step-finish","tokens":{"input":5,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO part VALUES ('pt2','m3','{"type":"step-finish","tokens":{"input":5,"output":10,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO part VALUES ('pt3','m3','{"type":"tool","tool":"bash"}')`,
		`INSERT INTO message VALUES ('m4','s1',1790100200000,'{"role":"assistant","modelID":"x"}')`,
		`INSERT INTO message VALUES ('m5','s1',1790100300000,'this is not json')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return db
}

func TestOpenCodeSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := makeOpenCodeDB(t, path)
	db.Close()
	o := opencodeFamily[0]
	evs, st, err := o.ParsePath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events (user, empty and malformed messages skipped), got %d: %+v", len(evs), evs)
	}
	m2 := find(evs, "claude-sonnet-5-5")
	// reasoning is billed as output and kept as a subset of it: 200 + 50.
	if m2.Input != 10 || m2.CacheRead != 1000 || m2.CacheWrite != 100 || m2.Output != 250 || m2.Reasoning != 50 || m2.Total() != 1360 || m2.Project != "/work/oc" || m2.Session != "s1" {
		t.Errorf("m2: %+v", m2)
	}
	m3 := find(evs, "gpt-5.4")
	if m3 == nil || m3.Input != 10 || m3.Output != 15 || m3.Total() != 25 {
		t.Errorf("a message with empty tokens must fall back to the sum of its step-finish parts: %+v", m3)
	}
	if st.Malformed != 1 {
		t.Errorf("malformed=%d", st.Malformed)
	}
}

func TestOpenCodeLegacyJSONMessage(t *testing.T) {
	evs, _ := parseFixture(t, opencodeFamily[0], "opencode/storage/message/s-legacy/msg_1.json")
	if len(evs) != 1 || evs[0].Total() != 12 || evs[0].Project != "/work/legacy" || evs[0].Model != "gpt-5" {
		t.Errorf("%+v", evs)
	}
}

func TestOpenCodeDetectAndFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENCODE_HOME", dir)
	if d := opencodeFamily[0].Detect(); d.Status != Ready && d.Status != NoData {
		t.Errorf("empty dir: %+v", d)
	}
	db := makeOpenCodeDB(t, filepath.Join(dir, "opencode.db"))
	db.Close()
	if fs := opencodeFamily[0].Files(); len(fs) != 1 {
		t.Errorf("files: %v", fs)
	}
	if d := opencodeFamily[0].Detect(); d.Status != Ready || d.Files != 1 {
		t.Errorf("%+v", d)
	}
	t.Setenv("OPENCODE_HOME", filepath.Join(dir, "missing"))
	if d := opencodeFamily[0].Detect(); d.Status != NotFound {
		t.Errorf("%+v", d)
	}
	var _ PathReader = opencodeFamily[0]
	var _ model.Event
}

func TestCorruptDatabaseIsAnErrorNotACrash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "opencode.db")
	if err := writeFile(p, "this is not a sqlite database"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := opencodeFamily[0].ParsePath(context.Background(), p); err == nil {
		t.Error("a corrupt database must return an error (reported as an unreadable file), not data")
	}
}

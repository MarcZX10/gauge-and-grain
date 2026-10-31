package providers

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range ents {
		n = append(n, e.Name())
	}
	return n
}

// Reading another tool's database must not create files beside it.
func TestOpenCodeNeverWritesBesideTheDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.db")
	db := makeOpenCodeDB(t, path)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	db.Close() // closing the last connection removes the -wal: the normal state
	before := dirNames(t, dir)
	evs, _, err := opencodeFamily[0].ParsePath(context.Background(), path)
	if err != nil || len(evs) != 2 {
		t.Fatalf("%v %d", err, len(evs))
	}
	if after := dirNames(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("files appeared next to the database: %v -> %v", before, after)
	}
}

// With a live write-ahead log the freshest rows are read from a private copy.
func TestOpenCodeReadsActiveWALFromACopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.db")
	db := makeOpenCodeDB(t, path)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES ('m9','s1',1790100400000,'{"role":"assistant","modelID":"fresh","tokens":{"input":7,"output":3}}')`); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() == 0 {
		t.Skip("this SQLite build did not leave a write-ahead log")
	}
	before := dirNames(t, dir)
	evs, _, err := opencodeFamily[0].ParsePath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if find(evs, "fresh") == nil {
		t.Errorf("a row that exists only in the -wal was not read: %+v", evs)
	}
	if after := dirNames(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("files appeared next to the database: %v -> %v", before, after)
	}
}

func TestOpenCodeSkipsHugeRowsAndStopsRunawayQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := makeOpenCodeDB(t, path)
	big := `{"role":"assistant","modelID":"big","tokens":{"input":1,"output":1},"pad":"` + strings.Repeat("x", maxRowData) + `"}`
	if _, err := db.Exec(`INSERT INTO message VALUES ('mbig','s1',1790100500000,?)`, big); err != nil {
		t.Fatal(err)
	}
	db.Close()
	evs, st, err := opencodeFamily[0].ParsePath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if find(evs, "big") != nil || st.Oversized != 1 || len(evs) != 2 {
		t.Errorf("an oversized row must be skipped and counted: oversized=%d evs=%+v", st.Oversized, evs)
	}
}

// A read error part way through a table must fail the database, not leave the
// rows read so far as its totals: the scan would cache those as complete. Here
// the bad row is in part, after two good step-finish rows, so the error comes
// from inside the loop.
func TestOpenCodeReadErrorMidTableIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := makeOpenCodeDB(t, path)
	if _, err := db.Exec(`INSERT INTO part VALUES ('pt9','m3','not json')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, _, err := opencodeFamily[0].ParsePath(context.Background(), path); err == nil {
		t.Error("a read error in the step-finish loop must be returned, not swallowed")
	}
}

// A copy left behind by a crash is removed once it is a day old; a fresh copy,
// and anything not named as a copy, stays.
func TestOpenCodeStaleCopiesAreRemoved(t *testing.T) {
	tmp := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{"gg-sqlite-old", "other-old"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(tmp, d), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmp, "gg-sqlite-new"), 0o755); err != nil {
		t.Fatal(err)
	}
	removeStaleCopies(tmp, time.Now())
	got := dirNames(t, tmp)
	if strings.Join(got, ",") != "gg-sqlite-new,other-old" {
		t.Errorf("after the sweep: %v", got)
	}
}

// A database that does not fit the copy limit is an error, not a truncated copy.
func TestOpenCodeCopyOverLimitIsAnError(t *testing.T) {
	saved := maxDBCopy
	defer func() { maxDBCopy = saved }()
	maxDBCopy = 10
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, "src"), strings.Repeat("x", 10)); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "src"), filepath.Join(dir, "at-limit")); err != nil {
		t.Errorf("a file at the limit must copy: %v", err)
	}
	if err := writeFile(filepath.Join(dir, "big"), strings.Repeat("x", 11)); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "big"), filepath.Join(dir, "copy")); err == nil {
		t.Error("a file over the limit must be an error")
	}
}

// A message row over the limit is not read, but its step-finish parts still
// give its tokens (counted with the model unknown). An oversized step part is
// not counted and is reported as oversized.
func TestOpenCodeOversizedRowsUseStepTokensAndCountParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := makeOpenCodeDB(t, path)
	pad := strings.Repeat("x", maxRowData)
	bigMsg := `{"role":"assistant","modelID":"big","pad":"` + pad + `"}`
	bigStep := `{"type":"step-finish","tokens":{"input":1,"output":1},"pad":"` + pad + `"}`
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO message VALUES ('mbig','s1',1790100500000,?)`, []any{bigMsg}},
		{`INSERT INTO part VALUES ('pbig1','mbig','{"type":"step-finish","tokens":{"input":7,"output":3,"cache":{"read":2}}}')`, nil},
		{`INSERT INTO part VALUES ('pbig2','m2',?)`, []any{bigStep}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	evs, st, err := opencodeFamily[0].ParsePath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var got *model.Event
	for i := range evs {
		if evs[i].Key == "opencode:s1:mbig" {
			got = &evs[i]
		}
	}
	if got == nil || got.Input != 7 || got.Output != 3 || got.CacheRead != 2 || got.Model != "unknown" || got.Project != "/work/oc" {
		t.Errorf("oversized row must take the tokens of its step parts: %+v", got)
	}
	// the message row and the oversized step part of m2 are both counted as oversized
	if st.Oversized != 2 {
		t.Errorf("oversized=%d, want 2 (the row and the part)", st.Oversized)
	}
	if find(evs, "claude-sonnet-5-5") == nil {
		t.Error("m2 must still be counted from its own tokens")
	}
}

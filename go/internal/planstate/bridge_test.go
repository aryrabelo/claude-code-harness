package planstate

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// fixture mirrors the real ~/.planqueue/backlog.db columns the bridge reads.
const pqFixtureDDL = `
CREATE TABLE epics(
  id TEXT PRIMARY KEY, title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  priority TEXT NOT NULL DEFAULT 'medium',
  repo TEXT, level TEXT NOT NULL DEFAULT 'epic',
  parent_id TEXT, sort_order INTEGER, note_path TEXT);
CREATE TABLE epic_deps(
  epic_id TEXT NOT NULL, depends_on_id TEXT NOT NULL,
  PRIMARY KEY(epic_id, depends_on_id));
INSERT INTO epics VALUES
  ('ep-1','roadmap goal','pending','high',NULL,'roadmap',NULL,1,NULL),
  ('ep-2','epic for repo','pending','high','claude-code-harness','epic','ep-1',2,NULL),
  ('t-1','task one','pending','medium','claude-code-harness','task','ep-2',3,NULL),
  ('t-2','task two done','done','medium','claude-code-harness','task','ep-2',4,NULL);
INSERT INTO epic_deps VALUES('t-1','t-2');
`

func pqFixture(t *testing.T, ddl string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backlog.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("fixture ddl: %v", err)
	}
	return path
}

func TestBridge_NotConfigured(t *testing.T) {
	b, err := OpenBridge(filepath.Join(t.TempDir(), "absent.db"))
	if err != nil {
		t.Fatalf("OpenBridge must not error on absent DB: %v", err)
	}
	if b.Mode() != BridgeNotConfigured {
		t.Fatalf("want not-configured, got %v", b.Mode())
	}
	_ = b.Close()
}

func TestBridge_ReadTasks(t *testing.T) {
	path := pqFixture(t, pqFixtureDDL)
	b, err := OpenBridge(path)
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	defer b.Close()
	if b.Mode() != BridgeReadWrite {
		t.Fatalf("want read-write mode, got %v", b.Mode())
	}
	items, err := b.Epics("claude-code-harness")
	if err != nil {
		t.Fatalf("Epics: %v", err)
	}
	if len(items) != 3 { // ep-2, t-1, t-2 (repo-scoped; roadmap has NULL repo)
		t.Fatalf("want 3 repo rows, got %d: %v", len(items), items)
	}
}

// Missing expected column -> degrade to read-only advisory, never crash.
func TestBridge_SchemaDrift_DegradesReadOnly(t *testing.T) {
	ddl := `CREATE TABLE epics(id TEXT PRIMARY KEY, title TEXT); CREATE TABLE epic_deps(epic_id TEXT, depends_on_id TEXT);`
	path := pqFixture(t, ddl)
	b, err := OpenBridge(path)
	if err != nil {
		t.Fatalf("OpenBridge must not crash on drifted schema: %v", err)
	}
	defer b.Close()
	if b.Mode() != BridgeDegraded {
		t.Fatalf("want degraded mode, got %v", b.Mode())
	}
	if err := b.RecordEvidence("t-1", "sess", "host", `{"commit":"abc"}`); err == nil {
		t.Fatalf("evidence write must be refused in degraded mode")
	}
}

// Sidecar evidence table is created by the bridge and never touches epics schema.
func TestBridge_SidecarEvidence(t *testing.T) {
	path := pqFixture(t, pqFixtureDDL)
	b, _ := OpenBridge(path)
	defer b.Close()
	if err := b.RecordEvidence("t-1", "sess-1", "claude", `{"commit":"abc1234"}`); err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM harness_run_evidence WHERE epic_id='t-1'`).Scan(&n); err != nil {
		t.Fatalf("sidecar query: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 evidence row, got %d", n)
	}
	// epics schema untouched (no new columns)
	rows, _ := db.Query(`PRAGMA table_info(epics)`)
	cols := 0
	for rows.Next() {
		cols++
	}
	rows.Close()
	if cols != 9 {
		t.Fatalf("epics schema changed: %d cols", cols)
	}
}

// One-way reconciliation: done tasks in Plans.md whose linked pq epic is still
// pending become PROPOSALS (never direct writes).
func TestBridge_DoneProposals(t *testing.T) {
	path := pqFixture(t, pqFixtureDDL)
	b, _ := OpenBridge(path)
	defer b.Close()
	props, err := b.DoneProposals(map[string]bool{"t-1": true, "t-2": true})
	if err != nil {
		t.Fatalf("DoneProposals: %v", err)
	}
	// t-1 pending in pq but done in Plans.md -> proposal; t-2 already done -> none
	if len(props) != 1 || props[0] != "t-1" {
		t.Fatalf("want [t-1], got %v", props)
	}
	// verify nothing was written
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var status string
	_ = db.QueryRow(`SELECT status FROM epics WHERE id='t-1'`).Scan(&status)
	if status != "pending" {
		t.Fatalf("bridge must not write epics.status; got %q", status)
	}
}

func TestExtractPqLinks(t *testing.T) {
	md := "| 1.1 | do thing <!-- pq:t-1 --> | dod | - | cc:done |\n| 1.2 | other | dod | - | cc:todo |"
	links := ExtractPqLinks(md)
	if len(links) != 1 || links["1.1"] != "t-1" {
		t.Fatalf("want {1.1:t-1}, got %v", links)
	}
	_ = os.Environ // keep os import if unused elsewhere
}

func TestLoadPlanqueueConfig(t *testing.T) {
	dir := t.TempDir()
	// no harness.toml -> disabled
	if cfg := LoadPlanqueueConfig(dir); cfg.Enabled {
		t.Fatalf("want disabled without harness.toml")
	}
	// section present, enabled, custom path
	toml := "[project]\nname = \"x\"\n\n[planqueue]\nenabled = true\npath = \"/tmp/x/backlog.db\"\n"
	if err := os.WriteFile(filepath.Join(dir, "harness.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadPlanqueueConfig(dir)
	if !cfg.Enabled || cfg.Path != "/tmp/x/backlog.db" {
		t.Fatalf("got %+v", cfg)
	}
	// enabled=false respected
	toml2 := "[planqueue]\nenabled = false\n"
	_ = os.WriteFile(filepath.Join(dir, "harness.toml"), []byte(toml2), 0o644)
	if cfg := LoadPlanqueueConfig(dir); cfg.Enabled {
		t.Fatalf("want disabled")
	}
}

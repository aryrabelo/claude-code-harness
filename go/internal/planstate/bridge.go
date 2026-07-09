package planstate

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"time"
)

// Bridge is a read-mostly client of the operator's global planqueue DB
// (~/.planqueue/backlog.db). The harness is a CLIENT, never the schema owner:
// it never ALTERs planqueue tables, asserts expected columns at open, and
// degrades to read-only advisory when the schema has drifted (the .bak-pre-*
// history shows planqueue migrates by hand; not_observed != absent).
//
// The only write surface is the additive sidecar table harness_run_evidence,
// which survives planqueue's own migrations.
type Bridge struct {
	db   *sql.DB
	mode BridgeMode
}

// BridgeMode is the tri-state availability of the planqueue bridge.
type BridgeMode int

const (
	// BridgeNotConfigured: DB absent — silent no-op, never a warning.
	BridgeNotConfigured BridgeMode = iota
	// BridgeDegraded: DB present but expected columns missing — read what is
	// readable, refuse all writes.
	BridgeDegraded
	// BridgeReadWrite: schema as expected — reads + sidecar evidence writes.
	BridgeReadWrite
)

func (m BridgeMode) String() string {
	switch m {
	case BridgeNotConfigured:
		return "not-configured"
	case BridgeDegraded:
		return "degraded"
	default:
		return "read-write"
	}
}

// epicsRequiredCols are the columns the bridge reads. Missing any -> degraded.
var epicsRequiredCols = []string{"id", "title", "status", "priority", "repo", "level", "parent_id"}

// OpenBridge opens the planqueue DB read-mostly. Absent DB is not an error.
func OpenBridge(path string) (*Bridge, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Bridge{mode: BridgeNotConfigured}, nil
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return &Bridge{mode: BridgeNotConfigured}, nil
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return &Bridge{mode: BridgeNotConfigured}, nil
	}

	// Boot assert: PRAGMA table_info on epics; missing column -> degraded.
	have := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(epics)`)
	if err != nil {
		db.Close()
		return &Bridge{mode: BridgeNotConfigured}, nil
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil {
			have[name] = true
		}
	}
	rows.Close()

	mode := BridgeReadWrite
	for _, c := range epicsRequiredCols {
		if !have[c] {
			mode = BridgeDegraded
			break
		}
	}
	return &Bridge{db: db, mode: mode}, nil
}

// Mode reports the bridge tri-state.
func (b *Bridge) Mode() BridgeMode { return b.mode }

// Close releases the underlying connection (safe on not-configured).
func (b *Bridge) Close() error {
	if b.db == nil {
		return nil
	}
	return b.db.Close()
}

// PqItem is one planqueue row scoped to a repo.
type PqItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Level    string `json:"level"`
	ParentID string `json:"parent_id,omitempty"`
}

// Epics returns planqueue rows for the given repo (all levels), read-only.
func (b *Bridge) Epics(repo string) ([]PqItem, error) {
	if b.mode == BridgeNotConfigured || b.db == nil {
		return nil, nil
	}
	if b.mode == BridgeDegraded {
		return nil, fmt.Errorf("planqueue bridge degraded: schema drift, reads limited")
	}
	rows, err := b.db.Query(`
		SELECT id, title, status, priority, level, COALESCE(parent_id,'')
		FROM epics WHERE repo=? ORDER BY sort_order, id`, repo)
	if err != nil {
		return nil, fmt.Errorf("planqueue epics: %w", err)
	}
	defer rows.Close()
	var out []PqItem
	for rows.Next() {
		var it PqItem
		if err := rows.Scan(&it.ID, &it.Title, &it.Status, &it.Priority, &it.Level, &it.ParentID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

const createSidecar = `
CREATE TABLE IF NOT EXISTS harness_run_evidence (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  epic_id       TEXT NOT NULL,
  session_id    TEXT NOT NULL DEFAULT '',
  host          TEXT NOT NULL DEFAULT '',
  evidence_json TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL
)`

// RecordEvidence appends harness evidence to the additive sidecar table.
// Refused in degraded/not-configured mode; never touches planqueue tables.
func (b *Bridge) RecordEvidence(epicID, sessionID, host, evidenceJSON string) error {
	if b.mode != BridgeReadWrite || b.db == nil {
		return fmt.Errorf("planqueue bridge %s: evidence write refused", b.mode)
	}
	if _, err := b.db.Exec(createSidecar); err != nil {
		return fmt.Errorf("planqueue sidecar ddl: %w", err)
	}
	if _, err := b.db.Exec(`
		INSERT INTO harness_run_evidence(epic_id,session_id,host,evidence_json,created_at)
		VALUES(?,?,?,?,?)`,
		epicID, sessionID, host, evidenceJSON, time.Now().Unix()); err != nil {
		return fmt.Errorf("planqueue sidecar insert: %w", err)
	}
	return nil
}

// DoneProposals returns planqueue epic ids that are still pending but whose
// linked Plans.md task is done. These are PROPOSALS for the human-gated
// harness-sync flow — the bridge never writes epics.status itself
// (spec.md clause 4).
func (b *Bridge) DoneProposals(doneEpicIDs map[string]bool) ([]string, error) {
	if b.mode == BridgeNotConfigured || b.db == nil {
		return nil, nil
	}
	if b.mode == BridgeDegraded {
		return nil, fmt.Errorf("planqueue bridge degraded: proposals unavailable")
	}
	var out []string
	for id, done := range doneEpicIDs {
		if !done {
			continue
		}
		var status string
		err := b.db.QueryRow(`SELECT status FROM epics WHERE id=?`, id).Scan(&status)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("planqueue proposal %s: %w", id, err)
		}
		if status != "done" {
			out = append(out, id)
		}
	}
	return out, nil
}

// rePqLink matches the Plans.md task link marker: <!-- pq:<epic_id> -->
var rePqLink = regexp.MustCompile(`^\|\s*([^|\s]+)\s*\|.*<!--\s*pq:([A-Za-z0-9_-]+)\s*-->`)

// ExtractPqLinks scans Plans.md content for task rows carrying a
// <!-- pq:<epic_id> --> link and returns taskID -> epicID.
func ExtractPqLinks(content string) map[string]string {
	out := map[string]string{}
	for _, line := range splitLines(content) {
		if m := rePqLink.FindStringSubmatch(line); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

package planstate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/Chachamaru127/claude-code-harness/go/internal/plans"
)

// Node is one projected Plans.md task row.
type Node struct {
	PlanName  string `json:"plan_name"`
	ID        string `json:"id"`
	ParentID  string `json:"parent_id,omitempty"`
	Level     string `json:"level"`
	Title     string `json:"title"`
	DoD       string `json:"dod"`
	Marker    string `json:"marker"`
	Parallel  bool   `json:"parallel"`
	PlansHash string `json:"plans_hash"`
}

// DriftRow reports a Plans.md row whose content no longer matches the
// projected hash (Plans.md changed since the last Reindex).
type DriftRow struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // changed | added | removed
	OldHash string `json:"old_hash,omitempty"`
	NewHash string `json:"new_hash,omitempty"`
}

// HealthStatus is the tri-state health contract
// (.claude/rules/active-watching-test-policy.md): an absent DB is
// not-configured, healthy, and silent — never a warning.
type HealthStatus struct {
	Healthy bool   `json:"healthy"`
	Reason  string `json:"reason"` // "" | not-configured | corrupted
}

// Store wraps the plan-state SQLite database.
type Store struct {
	db   *sql.DB
	path string
}

// DefaultRelPath is the project-relative DB location. `.harness/` is
// gitignored; the DB is machine-local and never committed (spec.md clause 3).
const DefaultRelPath = ".harness/plan_state.db"

// Status reports tri-state health without creating the DB.
func Status(path string) (HealthStatus, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return HealthStatus{Healthy: true, Reason: "not-configured"}, nil
	}
	db, err := open(path)
	if err != nil {
		return HealthStatus{Healthy: false, Reason: "corrupted"}, nil
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_meta`).Scan(&n); err != nil {
		return HealthStatus{Healthy: false, Reason: "corrupted"}, nil
	}
	return HealthStatus{Healthy: true, Reason: ""}, nil
}

// Open creates (if needed) and migrates the store at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("planstate: mkdir: %w", err)
	}
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	for _, ddl := range allDDL {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			return nil, fmt.Errorf("planstate: ddl: %w", err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO schema_meta(key,value) VALUES('version',?)
		 ON CONFLICT(key) DO NOTHING`, fmt.Sprint(SchemaVersion)); err != nil {
		db.Close()
		return nil, fmt.Errorf("planstate: schema_meta: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("planstate: open: %w", err)
	}
	// Pin the pool to one physical connection so the PRAGMAs below apply to
	// every statement (database/sql binds Exec'd PRAGMAs to a single
	// connection). Mirrors go/internal/state/store.go.
	db.SetMaxOpenConns(1)
	// WAL for concurrent readers; busy_timeout so concurrent claimers
	// serialize instead of failing with SQLITE_BUSY.
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("planstate: %s: %w", pragma, err)
		}
	}
	return db, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// rowHash hashes the definitional cells of a task row (drift detector).
// SHA-256 truncated to 8 bytes: deliberate non-security tradeoff — collision
// risk is negligible at Plans.md scale and short hashes keep drift output readable.
func rowHash(t plans.Task) string {
	h := sha256.Sum256([]byte(t.TaskID + "\x1f" + t.Title + "\x1f" + t.DoD + "\x1f" + t.Depends + "\x1f" + t.Status))
	return hex.EncodeToString(h[:8])
}

// marker maps plans.Tags to the stored marker enum.
func marker(t plans.Task) string {
	switch {
	case t.Tags.Done:
		return "done"
	case t.Tags.Wip:
		return "wip"
	case t.Tags.Blocked:
		return "blocked"
	case t.Tags.Todo:
		return "todo"
	default:
		return "unknown"
	}
}

// parentOf derives the parent dotted id ("111.2.3" -> "111.2"; "111.2" -> "111").
func parentOf(id string) string {
	i := strings.LastIndex(id, ".")
	if i <= 0 {
		return ""
	}
	return id[:i]
}

var reParallel = "[P]"

// Reindex projects the given Plans.md rows into plan_nodes/plan_deps for
// planName. Idempotent; rows absent from tasks are removed (definitional
// tables only — plan_runs history is preserved).
func (s *Store) Reindex(planName string, tasks []plans.Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("planstate: begin: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	seen := make([]any, 0, len(tasks))
	for _, t := range tasks {
		if strings.TrimSpace(t.TaskID) == "" {
			continue
		}
		par := 0
		if strings.Contains(t.Title, reParallel) || strings.Contains(t.DoD, reParallel) {
			par = 1
		}
		if _, err := tx.Exec(`
			INSERT INTO plan_nodes(plan_name,id,parent_id,level,title,dod,marker,parallel,plans_hash,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(plan_name,id) DO UPDATE SET
			  parent_id=excluded.parent_id, level=excluded.level,
			  title=excluded.title, dod=excluded.dod, marker=excluded.marker,
			  parallel=excluded.parallel, plans_hash=excluded.plans_hash,
			  updated_at=excluded.updated_at`,
			planName, t.TaskID, parentOf(t.TaskID), "task", t.Title, t.DoD,
			marker(t), par, rowHash(t), now); err != nil {
			return fmt.Errorf("planstate: upsert node %s: %w", t.TaskID, err)
		}
		if _, err := tx.Exec(`DELETE FROM plan_deps WHERE plan_name=? AND node_id=?`,
			planName, t.TaskID); err != nil {
			return fmt.Errorf("planstate: clear deps %s: %w", t.TaskID, err)
		}
		for _, dep := range dependencyIDs(t.Depends) {
			if _, err := tx.Exec(`
				INSERT INTO plan_deps(plan_name,node_id,depends_on_id) VALUES(?,?,?)
				ON CONFLICT DO NOTHING`, planName, t.TaskID, dep); err != nil {
				return fmt.Errorf("planstate: insert dep %s->%s: %w", t.TaskID, dep, err)
			}
		}
		seen = append(seen, t.TaskID)
	}

	// Remove stale definitional rows (tasks deleted/archived in Plans.md).
	if len(seen) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(seen)), ",")
		args := append([]any{planName}, seen...)
		if _, err := tx.Exec(`DELETE FROM plan_nodes WHERE plan_name=? AND id NOT IN (`+ph+`)`, args...); err != nil {
			return fmt.Errorf("planstate: prune nodes: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM plan_deps WHERE plan_name=? AND node_id NOT IN (`+ph+`)`, args...); err != nil {
			return fmt.Errorf("planstate: prune deps: %w", err)
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM plan_nodes WHERE plan_name=?`, planName); err != nil {
			return fmt.Errorf("planstate: prune all nodes: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM plan_deps WHERE plan_name=?`, planName); err != nil {
			return fmt.Errorf("planstate: prune all deps: %w", err)
		}
	}
	return tx.Commit()
}

// dependencyIDs extracts dotted task ids from a Depends cell
// ("-", "111.1, 111.2", "Phase 92" -> phase refs are skipped here;
// phase-level gating stays a Plans.md/reviewer concern).
func dependencyIDs(depends string) []string {
	depends = strings.TrimSpace(depends)
	if depends == "" || depends == "-" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(depends, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "-" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(part), "phase") {
			continue
		}
		out = append(out, part)
	}
	return out
}

// Nodes returns all projected nodes for planName.
func (s *Store) Nodes(planName string) ([]Node, error) {
	rows, err := s.db.Query(`
		SELECT plan_name,id,COALESCE(parent_id,''),level,title,dod,marker,parallel,plans_hash
		FROM plan_nodes WHERE plan_name=? ORDER BY id`, planName)
	if err != nil {
		return nil, fmt.Errorf("planstate: nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var par int
		if err := rows.Scan(&n.PlanName, &n.ID, &n.ParentID, &n.Level, &n.Title, &n.DoD, &n.Marker, &par, &n.PlansHash); err != nil {
			return nil, err
		}
		n.Parallel = par != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// Deps returns the dependency map node_id -> depends_on ids for planName.
func (s *Store) Deps(planName string) (map[string][]string, error) {
	rows, err := s.db.Query(`
		SELECT node_id,depends_on_id FROM plan_deps WHERE plan_name=? ORDER BY node_id,depends_on_id`, planName)
	if err != nil {
		return nil, fmt.Errorf("planstate: deps: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var n, d string
		if err := rows.Scan(&n, &d); err != nil {
			return nil, err
		}
		out[n] = append(out[n], d)
	}
	return out, rows.Err()
}

// Drift compares current Plans.md rows against the projected hashes.
// It is read-only: repairing drift is `Reindex` (Plans.md wins for
// definitions) or the human-gated harness-sync flow (for markers).
func (s *Store) Drift(planName string, tasks []plans.Task) ([]DriftRow, error) {
	stored := map[string]string{}
	rows, err := s.db.Query(`SELECT id,plans_hash FROM plan_nodes WHERE plan_name=?`, planName)
	if err != nil {
		return nil, fmt.Errorf("planstate: drift query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		stored[id] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var drift []DriftRow
	seen := map[string]bool{}
	for _, t := range tasks {
		if strings.TrimSpace(t.TaskID) == "" {
			continue
		}
		seen[t.TaskID] = true
		nh := rowHash(t)
		oh, ok := stored[t.TaskID]
		switch {
		case !ok:
			drift = append(drift, DriftRow{ID: t.TaskID, Kind: "added", NewHash: nh})
		case oh != nh:
			drift = append(drift, DriftRow{ID: t.TaskID, Kind: "changed", OldHash: oh, NewHash: nh})
		}
	}
	for id, oh := range stored {
		if !seen[id] {
			drift = append(drift, DriftRow{ID: id, Kind: "removed", OldHash: oh})
		}
	}
	return drift, nil
}

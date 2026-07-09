package planstate

import (
	"fmt"
	"syscall"
	"time"
)

// ClaimOpts parameterizes a task claim on this machine's run overlay.
type ClaimOpts struct {
	PID        int
	SessionID  string
	Assignee   string
	MachineID  string
	Worktree   string
	Wave       int
	StaleAfter time.Duration // a running claim older than this (by heartbeat) is stale
}

// Run is one row of the machine-local run overlay.
type Run struct {
	ID        int64  `json:"id"`
	PlanName  string `json:"plan_name"`
	NodeID    string `json:"node_id"`
	PID       int    `json:"run_pid"`
	SessionID string `json:"session_id"`
	Assignee  string `json:"assignee"`
	Worktree  string `json:"worktree"`
	Outcome   string `json:"outcome"`
}

// Claim atomically claims (planName, nodeID) for opts.PID.
//
// The claim is a compare-and-set: it succeeds only when no live run exists
// for the node. "Live" = outcome='running' AND heartbeat newer than
// StaleAfter. A stale running row is marked crashed inside the same
// transaction before the new claim is inserted, so two concurrent claimers
// serialize on the write transaction and exactly one wins (spec.md clause 3).
//
// Returns the new run id, or 0 when the node is already claimed.
func (s *Store) Claim(planName, nodeID string, opts ClaimOpts) (int64, error) {
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 2 * time.Hour
	}
	now := time.Now().Unix()
	cutoff := now - int64(opts.StaleAfter.Seconds())

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("planstate: claim begin: %w", err)
	}
	defer tx.Rollback()

	// Crash-mark stale running rows for this node (wall-clock backstop).
	if _, err := tx.Exec(`
		UPDATE plan_runs SET outcome='crashed', ended_at=?
		WHERE plan_name=? AND node_id=? AND outcome='running'
		  AND COALESCE(heartbeat_at, run_started, 0) < ?`,
		now, planName, nodeID, cutoff); err != nil {
		return 0, fmt.Errorf("planstate: claim stale-mark: %w", err)
	}

	// CAS: refuse when a live running row remains.
	var live int
	if err := tx.QueryRow(`
		SELECT count(*) FROM plan_runs
		WHERE plan_name=? AND node_id=? AND outcome='running'`,
		planName, nodeID).Scan(&live); err != nil {
		return 0, fmt.Errorf("planstate: claim liveness: %w", err)
	}
	if live > 0 {
		return 0, tx.Commit() // keep the stale-mark side effect
	}

	res, err := tx.Exec(`
		INSERT INTO plan_runs(plan_name,node_id,machine_id,session_id,wave,run_pid,assignee,worktree,run_started,heartbeat_at,outcome)
		VALUES(?,?,?,?,?,?,?,?,?,?,'running')`,
		planName, nodeID, opts.MachineID, opts.SessionID, opts.Wave,
		opts.PID, opts.Assignee, opts.Worktree, now, now)
	if err != nil {
		return 0, fmt.Errorf("planstate: claim insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("planstate: claim id: %w", err)
	}
	return id, tx.Commit()
}

// Heartbeat refreshes a running claim so long multi-turn tasks aren't reaped.
func (s *Store) Heartbeat(runID int64) error {
	_, err := s.db.Exec(`
		UPDATE plan_runs SET heartbeat_at=? WHERE id=? AND outcome='running'`,
		time.Now().Unix(), runID)
	if err != nil {
		return fmt.Errorf("planstate: heartbeat: %w", err)
	}
	return nil
}

// EndRun closes a run with outcome (done|abandoned|released|crashed) and
// appends evidence to run_log.
func (s *Store) EndRun(runID int64, outcome, evidence string) error {
	_, err := s.db.Exec(`
		UPDATE plan_runs
		SET outcome=?, ended_at=?, run_log=CASE WHEN ?='' THEN run_log ELSE run_log || ? || char(10) END
		WHERE id=?`,
		outcome, time.Now().Unix(), evidence, evidence, runID)
	if err != nil {
		return fmt.Errorf("planstate: end run: %w", err)
	}
	return nil
}

// pidAlive reports whether pid exists on this host (kill(pid, 0)).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	// EPERM = exists but not ours; still alive.
	return err == nil || err == syscall.EPERM
}

// Reap marks running rows as crashed when their pid is dead on this host or
// their heartbeat exceeds hardStale. Returns the number of rows reaped.
// Intended to run opportunistically (e.g. inside `plan next`) — no daemon.
func (s *Store) Reap(planName string, hardStale time.Duration) (int, error) {
	now := time.Now().Unix()
	cutoff := now - int64(hardStale.Seconds())
	rows, err := s.db.Query(`
		SELECT id, run_pid, COALESCE(heartbeat_at, run_started, 0)
		FROM plan_runs WHERE plan_name=? AND outcome='running'`, planName)
	if err != nil {
		return 0, fmt.Errorf("planstate: reap query: %w", err)
	}
	type victim struct{ id int64 }
	var victims []victim
	for rows.Next() {
		var id int64
		var pid int
		var hb int64
		if err := rows.Scan(&id, &pid, &hb); err != nil {
			rows.Close()
			return 0, err
		}
		if !pidAlive(pid) || hb < cutoff {
			victims = append(victims, victim{id})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, v := range victims {
		if _, err := s.db.Exec(`
			UPDATE plan_runs SET outcome='crashed', ended_at=?
			WHERE id=? AND outcome='running'`, now, v.id); err != nil {
			return 0, fmt.Errorf("planstate: reap mark: %w", err)
		}
	}
	return len(victims), nil
}

// Resumable lists running rows that look dead (dead pid or stale heartbeat) —
// candidates for re-dispatch after a crash.
func (s *Store) Resumable(planName string, staleAfter time.Duration) ([]Run, error) {
	cutoff := time.Now().Add(-staleAfter).Unix()
	rows, err := s.db.Query(`
		SELECT id, plan_name, node_id, COALESCE(run_pid,0), session_id, assignee, worktree, outcome,
		       COALESCE(heartbeat_at, run_started, 0)
		FROM plan_runs WHERE plan_name=? AND outcome='running'`, planName)
	if err != nil {
		return nil, fmt.Errorf("planstate: resumable: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var hb int64
		if err := rows.Scan(&r.ID, &r.PlanName, &r.NodeID, &r.PID, &r.SessionID, &r.Assignee, &r.Worktree, &r.Outcome, &hb); err != nil {
			return nil, err
		}
		if !pidAlive(r.PID) || hb < cutoff {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

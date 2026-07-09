// Package planstate provides the machine-local SQLite projection of Plans.md
// defined by spec.md "Plan State Projection Contract".
//
// Plans.md stays the definition SSOT; this DB is a rebuildable runtime overlay
// (plan nodes with content hashes, dependency projection, run records).
// Deleting the DB loses no truth: Reindex rebuilds every definitional row.
package planstate

// SchemaVersion is the current schema version, tracked in schema_meta.
const SchemaVersion = 1

const createSchemaMeta = `
CREATE TABLE IF NOT EXISTS schema_meta (
  key   TEXT NOT NULL PRIMARY KEY,
  value TEXT NOT NULL
)`

// plan_nodes mirrors one Plans.md task row per (plan_name, id).
// plans_hash is the drift detector: hash of the source row's definitional
// cells. marker maps the cc:* status family.
const createPlanNodes = `
CREATE TABLE IF NOT EXISTS plan_nodes (
  plan_name  TEXT NOT NULL DEFAULT 'default',
  id         TEXT NOT NULL,
  parent_id  TEXT,
  level      TEXT NOT NULL CHECK(level IN ('roadmap','phase','task')),
  title      TEXT NOT NULL DEFAULT '',
  dod        TEXT NOT NULL DEFAULT '',
  marker     TEXT NOT NULL CHECK(marker IN ('todo','wip','blocked','done','unknown')),
  parallel   INTEGER NOT NULL DEFAULT 0,
  plans_hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (plan_name, id)
)`

const createPlanDeps = `
CREATE TABLE IF NOT EXISTS plan_deps (
  plan_name     TEXT NOT NULL,
  node_id       TEXT NOT NULL,
  depends_on_id TEXT NOT NULL,
  PRIMARY KEY (plan_name, node_id, depends_on_id)
)`

// plan_runs is the machine-local run overlay (claims, pids, heartbeats).
// Never synced across machines; see spec.md clause 3.
const createPlanRuns = `
CREATE TABLE IF NOT EXISTS plan_runs (
  id           INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  plan_name    TEXT NOT NULL,
  node_id      TEXT NOT NULL,
  machine_id   TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  wave         INTEGER,
  run_pid      INTEGER,
  run_log      TEXT NOT NULL DEFAULT '',
  worktree     TEXT NOT NULL DEFAULT '',
  assignee     TEXT NOT NULL DEFAULT '',
  run_started  INTEGER,
  heartbeat_at INTEGER,
  ended_at     INTEGER,
  outcome      TEXT NOT NULL DEFAULT 'running'
               CHECK(outcome IN ('running','done','crashed','abandoned','released'))
)`

const createRunIndexes = `
CREATE INDEX IF NOT EXISTS idx_plan_runs_live
  ON plan_runs(plan_name, node_id, outcome)`

var allDDL = []string{
	createSchemaMeta,
	createPlanNodes,
	createPlanDeps,
	createPlanRuns,
	createRunIndexes,
}

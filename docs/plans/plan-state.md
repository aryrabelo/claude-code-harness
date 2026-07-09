# Plan State Projection (sub-spec)

Sub-spec of `spec.md` "Plan State Projection Contract". Implements the
machine-local SQLite projection that keeps multi-wave work on track without
weakening `Plans.md` as the task SSOT.

## Authority split

| Fact | Authoritative source |
|---|---|
| Task identity, content, DoD, `Depends`, `[P]`, `cc:*` markers | `Plans.md` (git-committed, human-reviewed) |
| Runtime state-of-play: claims, pids, heartbeats, dispatched waves, worktrees, evidence | `.harness/plan_state.db` (machine-local, gitignored) |
| Wave/DAG ordering | Derived by topological sort — computed, never stored as authority |

Deleting the DB loses no truth: `harness plan reindex` rebuilds every
definitional row from `Plans.md`. Only pids/logs are lost, which is correct —
a crashed pid is meaningless on a fresh machine.

## Schema (v1, `go/internal/planstate/schema.go`)

- `plan_nodes(plan_name, id, parent_id, level, title, dod, marker, parallel, plans_hash, updated_at)` — one row per Plans.md task; `plans_hash` (truncated SHA-256 of the definitional cells) is the drift detector.
- `plan_deps(plan_name, node_id, depends_on_id)` — dependency projection.
- `plan_runs(id, plan_name, node_id, machine_id, session_id, wave, run_pid, run_log, worktree, assignee, run_started, heartbeat_at, ended_at, outcome)` — machine-local run overlay; outcome ∈ running/done/crashed/abandoned/released.
- `schema_meta(key, value)` — version gate (same pattern as `internal/state`).

Connections: `modernc.org/sqlite`, WAL, `busy_timeout=5000`,
`SetMaxOpenConns(1)` so PRAGMAs apply pool-wide.

## CLI verbs (`bin/harness plan ...`)

| Verb | Effect |
|---|---|
| `reindex [--plan N] [--file Plans.md]` | Project Plans.md rows into the DB (idempotent; prunes stale rows; preserves run history) |
| `waves [--json]` | Kahn topo-sort dispatch layers; done deps satisfied, blocked tasks gate their dependents, cycles excluded safely |
| `next [--json]` | First unblocked task; runs an opportunistic reap first (no daemon) |
| `drift [--json]` | added/changed/removed rows vs projected hashes (read-only; compares before reindex) |
| `run-begin <task> [--session S] [--assignee A] [--worktree W]` | Atomic CAS claim; refuses when a live run holds the task; stale runs are crash-marked in the same transaction |
| `heartbeat <runID>` | Refresh a running claim (hosts call this at turn end) |
| `run-end <runID> [--outcome done\|abandoned\|released] [--evidence TEXT]` | Close the run, append evidence to `run_log` |
| `resume [--json]` | Running rows with dead pid or stale heartbeat — re-dispatch candidates |
| `status [--json]` | Tri-state health: absent DB = `not-configured` (healthy, silent) |

`waves`/`next` reindex lazily first, so output always matches the current
`Plans.md`. Hosts (Claude / Codex / Cursor / omp) consume plan state ONLY via
these verbs — no host reads the SQLite file directly. The omp shim
(`templates/omp/harness-extension.ts`) exposes them as LLM-callable tools
`plan_next` / `plan_claim` / `plan_done` and wires `session_start` wave
context, `turn_end` heartbeat, and `agent_end` auto-release.

## Claim protocol

Single-transaction compare-and-set: stale running rows (heartbeat older than
`StaleAfter`, default 2h) are crash-marked, then the claim inserts only when
no live run remains. Two concurrent claimers serialize on the write
transaction; exactly one wins (tested with `-race`). Reaping runs
opportunistically inside `plan next`: dead pid on this host (`kill(pid,0)`,
EPERM counts as alive) or hard wall-clock staleness → `crashed`.

## Reconciliation flow

- Plans.md changed → `drift` reports it; `reindex` repairs (Plans.md wins for
  definitions, always safe).
- Run finished but marker still `wip`/`todo` → surfaced as a PROPOSAL through
  the human-gated `harness-sync` flow. The projection layer never edits
  `Plans.md` silently.

## planqueue bridge (opt-in)

`harness.toml`:

```toml
[planqueue]
enabled = true
path = "~/.planqueue/backlog.db"  # default
```

The bridge (`go/internal/planstate/bridge.go`) is a CLIENT of the operator's
global plan queue, never the schema owner: it never `ALTER`s planqueue tables,
asserts required `epics` columns via `PRAGMA table_info` at open, and degrades
to read-only advisory on schema drift (planqueue migrates by hand —
`not_observed != absent`). Its only write surface is the additive sidecar
table `harness_run_evidence`. Plans.md tasks link to planqueue epics with
`<!-- pq:<epic_id> -->`; `cc:done` vs pq-`pending` mismatches are emitted as
harness-sync proposals only — the bridge never writes `epics.status`.

## Multi-machine: federated, not replicated

- The DB file is never committed or merged (WAL + binary merge = corruption);
  `.harness/` is gitignored.
- Claims are host-local by construction: pids and worktrees mean nothing on
  another machine, so cross-machine double-claim is structurally impossible.
- Cross-machine coordination = git-synced `Plans.md`, then `plan reindex` on
  the other machine.
- Distributed SQLite (rqlite/dqlite) is rejected as over-engineering for a
  per-project ledger; litestream is acceptable as backup/DR only, never as a
  multi-master sync layer.

## Tests

- `go/internal/planstate/*_test.go` — 23 tests: reindex idempotence, drift,
  rebuild-after-delete, tri-state status, wave layering, cycle safety,
  concurrent single-winner claim (with `-race`), stale takeover, reap,
  resumable, bridge schema-drift degrade, sidecar isolation,
  proposals-not-writes, config gate.
- `go/cmd/harness/plan_test.go` — verb-set delegation + e2e JSON schema test.
- `tests/test-omp-adapter-candidate.sh` — 26 gates including the plan-state
  tool wiring.

# oh-my-pi (omp) Host Adapter — Candidate Tier

Research + wiring record for running Claude Code Harness under
[oh-my-pi](https://github.com/can1357/oh-my-pi) (`omp`). Tier stays
**candidate**: no `supported` claim, no full Claude SessionStart/PreToolUse
parity, enforcement path materialized and tested but not battle-run.

## Findings (from omp docs/source, 2026-07-09)

| Surface | omp mechanism | Harness wiring needed |
|---|---|---|
| Hooks | TS extension modules (`export default (pi) => ...`), NOT hooks.json. Discovered from `<project>/.omp/extensions/`, `~/.omp/agent/extensions/`, `config.yml extensions:`, `--extension/-e` | Yes — shim (`templates/omp/harness-extension.ts`) |
| Pre-tool enforcement | `pi.on("tool_call")` → return `{ block, reason }`. Block/allow only, cannot mutate input, no "ask" | Shim maps omp lowercase tool ids → Claude names, pipes Claude-shaped payload to `bin/harness hook pre-tool`, blocks on deny envelope or exit 2 |
| Turn/stop lifecycle | `session_start`, `turn_start/turn_end`, `agent_end`, `session_shutdown` | `session_start` → `hook session-monitor`; `agent_end` → `scripts/advisor-hook.sh` |
| Skills | `claude-plugins` provider (priority 70) auto-loads from `~/.claude/plugins/cache/` + `installed_plugins.json`; `agents` provider reads `.agents/skills/`; `opencode` provider reads `opencode/skills` | None — free |
| Context files | `claude` provider reads `CLAUDE.md` / `.claude/CLAUDE.md` (user + project) | None — free |
| Advisor | Native: `modelRoles.advisor` + `advisor.enabled` in `~/.omp/agent/config.yml` (per-turn, richer than our Stop-hook advisor) | Optional — prefer omp-native advisor inside omp; harness session advisor also exposed via `/harness-advisor` |
| exec API | `pi.exec(cmd, args, {timeout,cwd,signal})` — no stdin option | Shim pipes payload via `bash -c 'printf %s "$1" | ...' _ <payload>` (positional arg, no interpolation) |

## Design decision: zero Go changes

`go/internal/hookcodec` already normalizes permissive input and emits the
Claude deny envelope by default. The shim sends Claude-shaped payloads and
parses `hookSpecificOutput.permissionDecision`, so the candidate tier needs no
new host in the codec. A dedicated `--host omp` (with omp-native deny shape)
is the promotion path if/when the adapter graduates.

## Install

```bash
# project-level (recommended; .omp/ is a generated artifact — gitignore it)
scripts/setup-omp.sh --project /path/to/project

# user-level (all omp sessions)
scripts/setup-omp.sh --user

# validate only
scripts/setup-omp.sh --check
```

## Known gaps vs Claude host

- `tool_call` cannot express `ask` — harness `ask` verdicts degrade to deny-with-reason? No: shim currently blocks only on `deny`/exit 2; `ask` blocks too (conservative).
- No PermissionRequest / PreCompact-with-custom-prompt equivalents (omp has `session_before_compact`).
- Mode 2 delivery (inbox-check) not wired; `turn_start` is the natural attach point later.
- Fail-open on engine absence (parity with the valid_root bootstrap in `.claude-plugin/hooks.json`).

## Evidence

- `tests/test-omp-adapter-candidate.sh` — 16 gates (template structure, setup
  `--check`, temp-project install, placeholder substitution, engine presence,
  bun parse).
- omp docs read: `docs/hooks.md`, `docs/extensions.md`,
  `docs/extension-loading.md`, `docs/skills.md`, `docs/context-files.md`,
  `docs/advisor-watchdog.md`, `src/discovery/claude-plugins.ts`,
  `src/exec/exec.ts`.

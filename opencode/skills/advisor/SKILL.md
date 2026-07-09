---
name: advisor
description: "Configure the session advisor: a secondary lightweight model that reviews the session at turn end (Stop hook) and injects one concise advisory (nit/concern/blocker). Use when user says advisor, /advisor, enable advisor, advisor status, advisor model. Do NOT load for: the worker-facing advisor agent (advisor-request.v1), code review, or release."
---

# Session Advisor

Optional second model attached to the session, modeled after oh-my-pi's advisor/watchdog. On every Stop (turn end), the hook `scripts/advisor-hook.sh` sends the session delta (transcript tail + `git status` + working-tree diff) to a lightweight secondary model with the advisor system prompt (`${CLAUDE_SKILL_DIR}/references/advisor-system-prompt.md`). The model either stays silent (`NO_ADVICE`) or returns exactly one advisory:

```
ADVISOR[nit|concern|blocker]: <one concrete, terse piece of advice>
```

The advisory is surfaced back into the session as a `systemMessage`. It is advisory-only by default; with `block_on: "blocker"` an `ADVISOR[blocker]` blocks the stop.

This is distinct from the `claude-code-harness:advisor` **agent** (worker-invoked `advisor-request.v1` policy advisor). This skill manages the automatic, hook-driven session advisor.

## Config SSOT

Two layers, merged by the hook (json overrides toml, per key):

1. **`harness.toml` `[advisor]` section** — project defaults, checked into the repo:

```toml
[advisor]
enabled = true
backend = "codex"   # codex | claude | omp
model = "gpt-5.5"
block_on = "never"  # never | blocker
```

2. **`.claude/harness/advisor.json`** — runtime override, managed by this skill (`/advisor on|off|...`):

```json
{
  "enabled": false,
  "backend": "codex",
  "model": "gpt-5.5",
  "block_on": "never",
  "max_diff_lines": 400,
  "max_transcript_lines": 200
}
```

| Field | Values | Default | Meaning |
|---|---|---|---|
| `enabled` | `true`/`false` | `false` | Advisor opt-in. Hook exits silently when false or file absent |
| `backend` | `codex` / `claude` / `omp` | `codex` | `codex` goes through `scripts/codex-companion.sh task` (read-only, policy-compliant); `claude` uses `claude -p`; `omp` uses oh-my-pi CLI (`omp -p --no-tools --no-session`) |
| `model` | model id | `gpt-5.5` | Passed as `--model` to the backend |
| `block_on` | `never` / `blocker` | `never` | Whether an `ADVISOR[blocker]` blocks the Stop |
| `max_diff_lines` | int | `400` | Diff truncation |
| `max_transcript_lines` | int | `200` | Transcript tail size |

Default advisor = **Codex `gpt-5.5`**, read-only. Requires `openai/codex-plugin-cc` installed (`/codex:setup`); if the companion is unavailable the hook fails silent and the stop proceeds.

## Subcommands

Parse `$ARGUMENTS`; default with no argument = `status`.

| Command | Effect |
|---|---|
| `/advisor on` | Set `enabled: true` (create config with defaults if absent). Confirm backend availability: for `codex`, run `bash scripts/codex-companion.sh setup --json` and warn if unavailable |
| `/advisor off` | Set `enabled: false` |
| `/advisor status` | Show current config + whether backend is reachable |
| `/advisor model <id>` | Set `model` |
| `/advisor backend <codex\|claude\|omp>` | Set `backend` |
| `/advisor block-on <never\|blocker>` | Set `block_on` |

Implementation: read/write `.claude/harness/advisor.json` with Read/Write tools (create `.claude/harness/` if needed). After changing config, report the resulting JSON in 1-3 lines.

## Safety

- Advisor is **read-only**: codex backend never gets `--write`; it cannot mutate the workspace.
- Advisor output is untrusted model output; it is surfaced as a note, never auto-executed.
- Blocking stops is opt-in (`block_on: "blocker"`) — default never blocks.

## Related

- `agents/advisor.md` — worker-facing policy advisor (different mechanism)
- `.claude/rules/codex-cli-only.md` — companion-wrapper policy the hook follows
- `scripts/advisor-hook.sh` — the Stop hook implementation

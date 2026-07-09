#!/usr/bin/env bash
# advisor-hook.sh — Stop-hook session advisor (oh-my-pi style).
# Reads .claude/harness/advisor.json; when enabled, sends the session delta
# (transcript tail + git status + diff) to a secondary model and surfaces
# one advisory back into the session as a systemMessage.
#
# Config file (managed by the /advisor skill):
#   {"enabled": true, "backend": "codex", "model": "gpt-5.5",
#    "block_on": "never", "max_diff_lines": 400, "max_transcript_lines": 200}
#
# backend: codex (via scripts/codex-companion.sh, read-only) | claude (claude -p)
#          | omp (oh-my-pi CLI, -p --no-tools --no-session)
# block_on: never (default) | blocker — whether an ADVISOR[blocker] blocks stop.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

INPUT="$(cat 2>/dev/null || true)"

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$PWD}"
CONFIG="${PROJECT_DIR}/.claude/harness/advisor.json"   # runtime override (/advisor skill)
TOML="${PROJECT_DIR}/harness.toml"                     # project defaults: [advisor] section
[ -f "${CONFIG}" ] || [ -f "${TOML}" ] || exit 0

# Merged lookup: harness.toml [advisor] provides defaults, advisor.json overrides.
read_cfg() {
  python3 - "$TOML" "$CONFIG" "$1" "$2" <<'PY'
import json, sys
cfg = {}
try:
    import tomllib
    with open(sys.argv[1], "rb") as f:
        cfg.update(tomllib.load(f).get("advisor", {}))
except Exception:
    pass
try:
    cfg.update(json.load(open(sys.argv[2])))
except Exception:
    pass
v = cfg.get(sys.argv[3], sys.argv[4])
print("true" if v is True else "false" if v is False else v)
PY
}

ENABLED="$(read_cfg enabled false)"
[ "${ENABLED}" = "true" ] || exit 0

BACKEND="$(read_cfg backend codex)"
MODEL="$(read_cfg model gpt-5.5)"
BLOCK_ON="$(read_cfg block_on never)"
MAX_DIFF="$(read_cfg max_diff_lines 400)"
MAX_TRANSCRIPT="$(read_cfg max_transcript_lines 200)"

PROMPT_FILE="${ROOT}/skills/advisor/references/advisor-system-prompt.md"
[ -f "${PROMPT_FILE}" ] || exit 0

# Transcript tail from Stop hook payload (transcript_path), if available.
TRANSCRIPT_PATH="$(printf '%s' "${INPUT}" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("transcript_path",""))
except Exception: print("")' 2>/dev/null || true)"

TRANSCRIPT_TAIL=""
if [ -n "${TRANSCRIPT_PATH}" ] && [ -f "${TRANSCRIPT_PATH}" ]; then
  TRANSCRIPT_TAIL="$(tail -n "${MAX_TRANSCRIPT}" "${TRANSCRIPT_PATH}" 2>/dev/null || true)"
fi

GIT_STATUS="$(cd "${PROJECT_DIR}" && git status --short 2>/dev/null | head -50 || true)"
GIT_DIFF="$(cd "${PROJECT_DIR}" && git diff HEAD 2>/dev/null | head -n "${MAX_DIFF}" || true)"

DELTA_FILE="$(mktemp)"
trap 'rm -f "${DELTA_FILE}"' EXIT
{
  cat "${PROMPT_FILE}"
  printf '\n\n---\n\n<session-delta>\n'
  printf '<git-status>\n%s\n</git-status>\n\n' "${GIT_STATUS}"
  printf '<git-diff truncated-at="%s-lines">\n%s\n</git-diff>\n\n' "${MAX_DIFF}" "${GIT_DIFF}"
  if [ -n "${TRANSCRIPT_TAIL}" ]; then
    printf '<transcript-tail lines="%s">\n%s\n</transcript-tail>\n' "${MAX_TRANSCRIPT}" "${TRANSCRIPT_TAIL}"
  fi
  printf '</session-delta>\n'
} > "${DELTA_FILE}"

ADVICE=""
case "${BACKEND}" in
  codex)
    # Read-only by default (no --write). Policy: codex only via companion wrapper.
    ADVICE="$(cat "${DELTA_FILE}" | bash "${ROOT}/scripts/codex-companion.sh" task --model "${MODEL}" 2>/dev/null || true)"
    ;;
  claude)
    ADVICE="$(claude -p --model "${MODEL}" < "${DELTA_FILE}" 2>/dev/null || true)"
    ;;
  omp)
    # oh-my-pi CLI, non-interactive, read-only (no tools), ephemeral session.
    ADVICE="$(omp -p --no-tools --no-session --model "${MODEL}" "@${DELTA_FILE}" 2>/dev/null || true)"
    ;;
  *)
    exit 0
    ;;
esac

# Extract the advisory line; silence means allow stop with no message.
LINE="$(printf '%s' "${ADVICE}" | grep -E '^ADVISOR\[(nit|concern|blocker)\]:' | head -1 || true)"
[ -n "${LINE}" ] || exit 0

SEVERITY="$(printf '%s' "${LINE}" | sed -E 's/^ADVISOR\[([a-z]+)\].*/\1/')"

if [ "${BLOCK_ON}" = "blocker" ] && [ "${SEVERITY}" = "blocker" ]; then
  python3 - "$LINE" <<'PY'
import json, sys
print(json.dumps({"decision": "block", "reason": "Session advisor raised a blocker: " + sys.argv[1]}))
PY
else
  python3 - "$LINE" <<'PY'
import json, sys
print(json.dumps({"systemMessage": sys.argv[1]}))
PY
fi
exit 0

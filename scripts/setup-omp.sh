#!/usr/bin/env bash
# setup-omp.sh
# Install the Claude Code Harness host adapter shim for oh-my-pi (omp).
#
# omp does not read hooks.json; its hooks are TS extension modules discovered
# from <project>/.omp/extensions/ (or ~/.omp/agent/extensions/). This script
# materializes templates/omp/harness-extension.ts into that location with the
# plugin root resolved (FACT-1: generated build artifact, not tracked config).
#
# Skills and CLAUDE.md need no wiring: omp's `claude-plugins` / `claude`
# discovery providers load them from ~/.claude/plugins and .claude/CLAUDE.md.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${HARNESS_PROJECT_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"
TEMPLATE="${ROOT_DIR}/templates/omp/harness-extension.ts"
PLUGIN_NAME="claude-code-harness"

CHECK_ONLY=0
TARGET_PROJECT="$PWD"
USER_LEVEL=0

usage() {
  cat <<'EOF'
Usage: setup-omp.sh [--check] [--project <dir>] [--user]

Install the harness omp adapter shim (candidate tier).

Options:
  --check          Validate template + engine only; do not install.
  --project <dir>  Target project (default: current directory).
                   Installs to <dir>/.omp/extensions/harness.ts
  --user           Install to ~/.omp/agent/extensions/harness.ts instead
                   (applies to every omp session for this user/profile).
  -h, --help

Environment:
  HARNESS_PROJECT_ROOT  Repo/plugin root (default: parent of scripts/)
EOF
}

log_info() { echo "[INFO] $1"; }
log_ok() { echo "[OK]   $1"; }
log_err() { echo "[ERR]  $1" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --check) CHECK_ONLY=1 ;;
    --project) TARGET_PROJECT="${2:?--project requires a directory}"; shift ;;
    --user) USER_LEVEL=1 ;;
    -h|--help) usage; exit 0 ;;
    *) log_err "unknown argument: $1"; usage; exit 2 ;;
  esac
  shift
done

# --- validation -------------------------------------------------------------
[ -f "${TEMPLATE}" ] || { log_err "template not found: ${TEMPLATE}"; exit 1; }
grep -q "__HARNESS_ROOT__" "${TEMPLATE}" || { log_err "template missing __HARNESS_ROOT__ placeholder"; exit 1; }
grep -q 'pi.on("tool_call"' "${TEMPLATE}" || { log_err "template missing tool_call handler"; exit 1; }

if [ ! -x "${ROOT_DIR}/bin/harness" ]; then
  log_err "bin/harness not found or not executable under ${ROOT_DIR}"
  log_err "build it first (cd go && go build -o ../bin/harness ./cmd/harness) or install the plugin"
  exit 1
fi
log_ok "template and engine validated (root: ${ROOT_DIR})"

if ! command -v omp >/dev/null 2>&1; then
  log_info "omp CLI not found on PATH — install oh-my-pi first (shim will still be written)"
fi

if [ "${CHECK_ONLY}" -eq 1 ]; then
  log_ok "check passed"
  exit 0
fi

# --- install ----------------------------------------------------------------
if [ "${USER_LEVEL}" -eq 1 ]; then
  DEST_DIR="${HOME}/.omp/agent/extensions"
else
  DEST_DIR="${TARGET_PROJECT}/.omp/extensions"
fi
DEST="${DEST_DIR}/harness.ts"

mkdir -p "${DEST_DIR}"
sed "s|__HARNESS_ROOT__|${ROOT_DIR}|g" "${TEMPLATE}" > "${DEST}"
log_ok "installed ${DEST}"

if [ "${USER_LEVEL}" -eq 0 ]; then
  GITIGNORE="${TARGET_PROJECT}/.gitignore"
  if [ -f "${GITIGNORE}" ] && ! grep -qE '^\.omp/' "${GITIGNORE}"; then
    log_info "consider adding '.omp/' to ${GITIGNORE} (shim is a generated artifact)"
  fi
fi

log_info "omp auto-discovers ${DEST_DIR}/*.ts at startup — restart omp to load"
log_info "enforcement: tool_call → ${ROOT_DIR}/bin/harness hook pre-tool (R01-R13)"
log_info "advisor: /harness-advisor on|off|status inside omp (config: .claude/harness/advisor.json)"

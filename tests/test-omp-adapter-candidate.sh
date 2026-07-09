#!/usr/bin/env bash
# test-omp-adapter-candidate.sh
# Candidate-tier gates for the oh-my-pi (omp) host adapter shim.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
TEMPLATE="${ROOT_DIR}/templates/omp/harness-extension.ts"
SETUP="${ROOT_DIR}/scripts/setup-omp.sh"

PASS=0
FAIL=0
ok() { echo "[PASS] $1"; PASS=$((PASS + 1)); }
ng() { echo "[FAIL] $1"; FAIL=$((FAIL + 1)); }

# 1. Template structure
[ -f "${TEMPLATE}" ] && ok "template exists" || ng "template missing"
grep -q "__HARNESS_ROOT__" "${TEMPLATE}" && ok "placeholder present" || ng "placeholder missing"
grep -q 'pi.on("tool_call"' "${TEMPLATE}" && ok "tool_call handler" || ng "tool_call handler missing"
grep -q 'pi.on("session_start"' "${TEMPLATE}" && ok "session_start handler" || ng "session_start handler missing"
grep -q 'pi.on("agent_end"' "${TEMPLATE}" && ok "agent_end handler" || ng "agent_end handler missing"
grep -q "permissionDecision" "${TEMPLATE}" && ok "claude deny envelope parsed" || ng "deny envelope parse missing"
grep -q "block: true" "${TEMPLATE}" && ok "block contract" || ng "block contract missing"
# Policy: payload must never be string-interpolated into the shell command.
grep -q 'printf .%s. "\$1"' "${TEMPLATE}" && ok "stdin via positional arg" || ng "stdin plumbing missing"

# 2. Setup script --check
if bash "${SETUP}" --check >/dev/null 2>&1; then
  ok "setup --check passes"
else
  ng "setup --check failed"
fi

# 3. Install into a temp project resolves the placeholder
TMP_PROJ="$(mktemp -d)"
trap 'rm -rf "${TMP_PROJ}"' EXIT
if bash "${SETUP}" --project "${TMP_PROJ}" >/dev/null 2>&1; then
  ok "setup installs into project"
else
  ng "setup install failed"
fi
DEST="${TMP_PROJ}/.omp/extensions/harness.ts"
[ -f "${DEST}" ] && ok "shim written to .omp/extensions" || ng "shim not written"
if [ -f "${DEST}" ]; then
  if grep -q "__HARNESS_ROOT__" "${DEST}"; then
    ng "placeholder not substituted"
  else
    ok "placeholder substituted"
  fi
  grep -q "const HARNESS_ROOT = \"${ROOT_DIR}\"" "${DEST}" && ok "shim points at engine root" || ng "engine root missing in shim"
fi

# 4. Referenced entrypoints exist
[ -x "${ROOT_DIR}/bin/harness" ] && ok "bin/harness executable" || ng "bin/harness missing"
[ -f "${ROOT_DIR}/scripts/advisor-hook.sh" ] && ok "advisor-hook.sh present" || ng "advisor-hook.sh missing"

# 5. Optional: TS syntax check when bun is available
if command -v bun >/dev/null 2>&1; then
  if bun build --no-bundle "${DEST}" >/dev/null 2>&1 || bun -e "await import('${DEST}')" >/dev/null 2>&1; then
    ok "shim parses under bun"
  else
    ng "shim failed bun parse"
  fi
else
  echo "[SKIP] bun not available; TS parse check skipped"
fi

echo "----"
echo "Tests passed: ${PASS}, failed: ${FAIL}"
[ "${FAIL}" -eq 0 ]

/**
 * Claude Code Harness — oh-my-pi (omp) host adapter shim (candidate tier).
 *
 * Installed by scripts/setup-omp.sh into <project>/.omp/extensions/harness.ts
 * (a gitignored build artifact — edit the template in templates/omp/, not the
 * installed copy). The setup script replaces __HARNESS_ROOT__ with the
 * resolved plugin root.
 *
 * One policy engine, three+ hosts: this shim routes omp lifecycle events to
 * the SAME `bin/harness` Go engine used by Claude / Codex / Cursor hooks.
 * Payloads are sent in the Claude pre-tool shape (hookcodec normalizes), so
 * no Go changes are required for the candidate tier.
 *
 * Wired events:
 *   - tool_call      → bin/harness hook pre-tool  (R01-R13 enforcement; block on deny)
 *   - session_start  → bin/harness hook session-monitor (project status note)
 *                      + plan-state wave context (top ready tasks)
 *   - turn_end       → heartbeat for the active plan-state claim
 *   - agent_end      → scripts/advisor-hook.sh    (session advisor, opt-in)
 *                      + release the active claim if still open
 *   - /harness-advisor command → toggle .claude/harness/advisor.json
 *
 * Plan-state tools (LLM-callable, spec.md "Plan State Projection Contract"):
 *   - plan_next  → bin/harness plan next --json   (first unblocked task)
 *   - plan_claim → bin/harness plan run-begin ... (atomic CAS claim)
 *   - plan_done  → bin/harness plan run-end ...   (close claim with evidence)
 * The shim never opens the SQLite file directly — hosts consume via CLI only.
 */

const HARNESS_ROOT = "__HARNESS_ROOT__";
const HARNESS_BIN = `${HARNESS_ROOT}/bin/harness`;
const ADVISOR_HOOK = `${HARNESS_ROOT}/scripts/advisor-hook.sh`;

// omp tool ids are lowercase; the harness policy engine expects Claude names.
const TOOL_NAME_MAP: Record<string, string> = {
	bash: "Bash",
	edit: "Edit",
	write: "Write",
	read: "Read",
	grep: "Grep",
	glob: "Glob",
	find: "Glob",
	ls: "Read",
};

function mapToolName(name: string): string {
	return TOOL_NAME_MAP[name] ?? name;
}

// Pipe a JSON payload into a harness entrypoint via stdin. pi.exec has no
// stdin option, so route through bash with the payload as a positional
// argument (never interpolated into the command string).
async function runWithStdin(
	pi: any,
	payload: string,
	binary: string,
	args: string[],
	timeoutMs: number,
): Promise<{ stdout: string; code: number }> {
	const cmd = `printf '%s' "$1" | "$2" ${args.map((_, i) => `"$${i + 3}"`).join(" ")}`;
	const result = await pi.exec("/bin/bash", ["-c", cmd, "_", payload, binary, ...args], {
		timeout: timeoutMs,
	});
	return { stdout: result.stdout ?? "", code: result.code ?? 0 };
}

export default function harness(pi: any): void {
	// --- R01-R13 enforcement (PreToolUse equivalent) --------------------
	pi.on("tool_call", async (event: any) => {
		const payload = JSON.stringify({
			tool_name: mapToolName(String(event.toolName ?? "")),
			tool_input: event.input ?? {},
			cwd: process.cwd(),
		});
		try {
			const { stdout, code } = await runWithStdin(pi, payload, HARNESS_BIN, ["hook", "pre-tool"], 10_000);
			// Claude deny envelope: {"hookSpecificOutput":{"permissionDecision":"deny",...}}
			// Exit code 2 is the universal blocker across hosts.
			let reason = "";
			try {
				const parsed = JSON.parse(stdout || "{}");
				const spec = parsed.hookSpecificOutput ?? {};
				if (spec.permissionDecision === "deny" || spec.permissionDecision === "ask") {
					reason = String(spec.permissionDecisionReason ?? "blocked by harness policy");
				}
			} catch {
				// Non-JSON stdout: fall through to exit-code check.
			}
			if (!reason && code === 2) {
				reason = "blocked by harness policy (R01-R13)";
			}
			if (reason) {
				return { block: true, reason: `[claude-code-harness] ${reason}` };
			}
		} catch {
			// Engine unavailable: fail open (parity with the valid_root bootstrap
			// in .claude-plugin/hooks.json, which skips when the root is missing).
		}
		return undefined;
	});

	// Active plan-state claim for this session (set by plan_claim).
	let activeRunID: number | null = null;

	async function planCLI(args: string[], timeoutMs = 15_000): Promise<string> {
		const result = await pi.exec(HARNESS_BIN, ["plan", ...args], { timeout: timeoutMs });
		return (result.stdout ?? "").trim();
	}

	// --- SessionStart equivalent ----------------------------------------
	pi.on("session_start", async () => {
		try {
			const { stdout } = await runWithStdin(pi, "{}", HARNESS_BIN, ["hook", "session-monitor"], 10_000);
			const text = stdout.trim();
			if (text) {
				pi.sendMessage(
					{ customType: "harness-session-monitor", content: text, display: true },
					{ triggerTurn: false },
				);
			}
		} catch {
			// fail open
		}
		// Wave context: top ready tasks so every session opens on-track.
		try {
			const waves = JSON.parse(await planCLI(["waves", "--json"]));
			if (Array.isArray(waves) && waves.length > 0) {
				const top = waves[0].slice(0, 3).map((n: any) => `${n.id}: ${n.title}`).join("\n");
				pi.sendMessage(
					{
						customType: "harness-wave",
						content: `Current wave (ready tasks):\n${top}`,
						display: true,
					},
					{ triggerTurn: false },
				);
			}
		} catch {
			// no Plans.md / engine unavailable: silent
		}
	});

	// --- Heartbeat: keep the active claim alive across long turns --------
	pi.on("turn_end", async () => {
		if (activeRunID === null) return;
		try {
			await pi.exec(HARNESS_BIN, ["plan", "heartbeat", String(activeRunID)], { timeout: 5_000 });
		} catch {
			// fail open
		}
	});

	// --- Plan-state tools (LLM-callable) ----------------------------------
	if (typeof pi.registerTool === "function") {
		pi.registerTool({
			name: "plan_next",
			description: "Get the first unblocked task from Plans.md (deps satisfied, not claimed). Returns null when nothing is ready.",
			parameters: {},
			handler: async () => JSON.parse((await planCLI(["next", "--json"])) || "null"),
		});
		pi.registerTool({
			name: "plan_claim",
			description: "Atomically claim a Plans.md task for this session (CAS; fails if a live run already holds it). Returns {run_id, node_id}.",
			parameters: { taskId: { type: "string", description: "Dotted task id, e.g. 111.6" } },
			handler: async (input: any) => {
				const out = JSON.parse(
					(await planCLI(["run-begin", String(input.taskId), "--session", "omp", "--assignee", "omp", "--json"])) || "{}",
				);
				if (out.run_id) activeRunID = out.run_id;
				return out;
			},
		});
		pi.registerTool({
			name: "plan_done",
			description: "Close the claimed task run with an outcome (done|abandoned|released) and evidence (commits, test results).",
			parameters: {
				runId: { type: "number", description: "Run id from plan_claim (defaults to the active claim)" },
				outcome: { type: "string", description: "done | abandoned | released" },
				evidence: { type: "string", description: "Evidence: commit hashes, test output summary" },
			},
			handler: async (input: any) => {
				const id = input.runId ?? activeRunID;
				if (!id) return { error: "no active claim" };
				const out = await planCLI([
					"run-end", String(id),
					"--outcome", String(input.outcome ?? "done"),
					"--evidence", String(input.evidence ?? ""),
				]);
				if (id === activeRunID) activeRunID = null;
				return { result: out };
			},
		});
	}

	// --- Stop equivalent: session advisor (opt-in via /advisor config) ---
	pi.on("agent_end", async () => {
		// Release a still-open claim so a crashed/abandoned session never
		// holds the task (reap would catch it eventually; this is immediate).
		if (activeRunID !== null) {
			try {
				await planCLI(["run-end", String(activeRunID), "--outcome", "released", "--evidence", "agent_end auto-release"]);
			} catch {
				// reap backstop covers this
			}
			activeRunID = null;
		}
		try {
			const { stdout } = await runWithStdin(pi, "{}", "/bin/bash", [ADVISOR_HOOK], 120_000);
			const text = stdout.trim();
			if (!text) return;
			const parsed = JSON.parse(text);
			const note = parsed.systemMessage ?? parsed.reason;
			if (note) {
				pi.sendMessage(
					{ customType: "harness-advisor", content: String(note), display: true },
					{ triggerTurn: false },
				);
			}
		} catch {
			// advisor disabled/unavailable: silent no-op
		}
	});

	// --- /harness-advisor command ----------------------------------------
	if (typeof pi.registerCommand === "function") {
		pi.registerCommand("harness-advisor", {
			description: "Toggle/inspect the harness session advisor (on|off|status)",
			handler: async (args: any, ctx: any) => {
				const fs = await import("node:fs");
				const path = await import("node:path");
				const cfgPath = path.join(process.cwd(), ".claude", "harness", "advisor.json");
				const arg = String(args ?? "").trim();
				let cfg: any = {};
				try {
					cfg = JSON.parse(fs.readFileSync(cfgPath, "utf8"));
				} catch {
					cfg = { enabled: false, backend: "codex", model: "gpt-5.5", block_on: "never" };
				}
				if (arg === "on" || arg === "off") {
					cfg.enabled = arg === "on";
					fs.mkdirSync(path.dirname(cfgPath), { recursive: true });
					fs.writeFileSync(cfgPath, `${JSON.stringify(cfg, null, 2)}\n`);
				}
				pi.sendMessage(
					{
						customType: "harness-advisor-status",
						content: `harness advisor: ${JSON.stringify(cfg)}`,
						display: true,
					},
					{ triggerTurn: false },
				);
			},
		});
	}
}

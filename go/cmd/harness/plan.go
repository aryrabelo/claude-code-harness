package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Chachamaru127/claude-code-harness/go/internal/plans"
	"github.com/Chachamaru127/claude-code-harness/go/internal/planstate"
)

// planStateVerbs are the projection subcommands handled by runPlanState;
// any other `harness plan` invocation falls through to the legacy prompt verb.
var planStateVerbs = map[string]bool{
	"reindex": true, "waves": true, "next": true, "drift": true, "status": true,
}

// runPlanState implements the plan-state projection verbs (spec.md "Plan State
// Projection Contract"). Distinct from the legacy `plans` (check-deps) verb.
//
//	harness plan reindex [--plan N] [--file Plans.md] [--json]
//	harness plan waves   [--plan N] [--file Plans.md] [--json]
//	harness plan next    [--plan N] [--file Plans.md] [--json]
//	harness plan drift   [--plan N] [--file Plans.md] [--json]
//	harness plan status  [--json]
//
// waves/next/drift reindex lazily first, so they are always consistent with
// the current Plans.md (the DB is a projection, never a second authority).
func runPlanState(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: harness plan <reindex|waves|next|drift|status> [--plan NAME] [--file Plans.md] [--json]")
		os.Exit(1)
	}
	verb := args[0]
	planName := "default"
	file := "Plans.md"
	asJSON := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--plan":
			if i+1 < len(args) {
				planName = args[i+1]
				i++
			}
		case "--file":
			if i+1 < len(args) {
				file = args[i+1]
				i++
			}
		case "--json":
			asJSON = true
		}
	}

	dbPath := filepath.Join(".", planstate.DefaultRelPath)

	if verb == "status" {
		st, err := planstate.Status(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan status: %v\n", err)
			os.Exit(1)
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(st)
		} else if st.Reason == "" {
			fmt.Println("plan-state: healthy")
		} else {
			fmt.Printf("plan-state: %s\n", st.Reason)
		}
		if !st.Healthy {
			os.Exit(1)
		}
		return
	}

	tasks, err := plans.ParseFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan %s: failed to read %s: %v\n", verb, file, err)
		os.Exit(1)
	}

	store, err := planstate.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan %s: %v\n", verb, err)
		os.Exit(1)
	}
	defer store.Close()

	switch verb {
	case "reindex":
		if err := store.Reindex(planName, tasks); err != nil {
			fmt.Fprintf(os.Stderr, "plan reindex: %v\n", err)
			os.Exit(1)
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"plan": planName, "tasks": len(tasks), "db": dbPath,
			})
		} else {
			fmt.Printf("plan reindex OK: %s (%d tasks) -> %s\n", planName, len(tasks), dbPath)
		}

	case "drift":
		// Drift compares BEFORE reindexing (that is the whole point).
		drift, err := store.Drift(planName, tasks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan drift: %v\n", err)
			os.Exit(1)
		}
		if asJSON {
			out := drift
			if out == nil {
				out = []planstate.DriftRow{}
			}
			_ = json.NewEncoder(os.Stdout).Encode(out)
		} else if len(drift) == 0 {
			fmt.Println("plan drift: none")
		} else {
			for _, d := range drift {
				fmt.Printf("drift %s: %s\n", d.Kind, d.ID)
			}
		}

	case "waves", "next":
		if err := store.Reindex(planName, tasks); err != nil {
			fmt.Fprintf(os.Stderr, "plan %s: reindex: %v\n", verb, err)
			os.Exit(1)
		}
		if verb == "waves" {
			waves, err := store.Waves(planName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "plan waves: %v\n", err)
				os.Exit(1)
			}
			if asJSON {
				out := waves
				if out == nil {
					out = [][]planstate.Node{}
				}
				_ = json.NewEncoder(os.Stdout).Encode(out)
			} else {
				for i, wave := range waves {
					fmt.Printf("wave %d:", i+1)
					for _, n := range wave {
						fmt.Printf(" %s", n.ID)
					}
					fmt.Println()
				}
				if len(waves) == 0 {
					fmt.Println("no ready tasks")
				}
			}
			return
		}
		next, err := store.Next(planName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan next: %v\n", err)
			os.Exit(1)
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(next) // null when none ready
		} else if next == nil {
			fmt.Println("no ready tasks")
		} else {
			fmt.Printf("next: %s — %s\n", next.ID, next.Title)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown plan subcommand: %s\n", verb)
		os.Exit(1)
	}
}

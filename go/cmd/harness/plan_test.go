package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The projection subcommand set must stay in sync with runPlanState's switch
// and must not swallow the legacy no-arg `harness plan` prompt verb.
func TestPlanStateVerbs_Set(t *testing.T) {
	for _, v := range []string{"reindex", "waves", "next", "drift", "status"} {
		if !planStateVerbs[v] {
			t.Fatalf("planStateVerbs missing %q", v)
		}
	}
	for _, v := range []string{"", "--help", "somePrompt"} {
		if planStateVerbs[v] {
			t.Fatalf("planStateVerbs must not claim %q (legacy path)", v)
		}
	}
}

const planFixture = `# Test Plans.md

## Phase 1

| Task | Content | DoD | Depends | Status |
|------|---------|-----|---------|--------|
| 1.1 | base | done | - | cc:done |
| 1.2 | next up ` + "`[P]`" + ` | t | 1.1 | cc:todo |
| 1.3 | after | t | 1.2 | cc:todo |
`

// End-to-end verb schema check through the real binary path (go run of this
// package is heavy; use `go test`-built test binary via os/exec on self? No —
// build once with `go build` into t.TempDir()).
func TestPlanVerbs_JSONSchemas(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "harness-test-bin")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustCwd(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "Plans.md"), []byte(planFixture), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	run := func(args ...string) []byte {
		cmd := exec.Command(bin, args...)
		cmd.Dir = work
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}

	// status before any DB: not-configured
	var st struct {
		Healthy bool   `json:"healthy"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(run("plan", "status", "--json"), &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if !st.Healthy || st.Reason != "not-configured" {
		t.Fatalf("status: want not-configured, got %+v", st)
	}

	// next: 1.2 (1.1 done, 1.3 gated)
	var next struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(run("plan", "next", "--json"), &next); err != nil {
		t.Fatalf("next json: %v", err)
	}
	if next.ID != "1.2" {
		t.Fatalf("next: want 1.2, got %q", next.ID)
	}

	// waves: [[1.2],[1.3]]
	var waves [][]struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(run("plan", "waves", "--json"), &waves); err != nil {
		t.Fatalf("waves json: %v", err)
	}
	if len(waves) != 2 || waves[0][0].ID != "1.2" || waves[1][0].ID != "1.3" {
		t.Fatalf("waves: want [[1.2],[1.3]], got %v", waves)
	}

	// drift after implicit reindex: empty array
	var drift []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(run("plan", "drift", "--json"), &drift); err != nil {
		t.Fatalf("drift json: %v", err)
	}
	if len(drift) != 0 {
		t.Fatalf("drift: want none, got %v", drift)
	}

	// DB now exists inside work dir under .harness/
	if _, err := os.Stat(filepath.Join(work, ".harness", "plan_state.db")); err != nil {
		t.Fatalf("plan_state.db not created: %v", err)
	}
}

func mustCwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

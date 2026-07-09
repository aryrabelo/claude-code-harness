package planstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Chachamaru127/claude-code-harness/go/internal/plans"
)

func sampleTasks() []plans.Task {
	return []plans.Task{
		{TaskID: "111.1", Title: "`[lane:gate]` spec delta", DoD: "spec section present", Depends: "-", Status: "cc:done [b8bf532]", Tags: plans.Tags{Done: true}},
		{TaskID: "111.2", Title: "`[lane:gate]` `[P]` planstate pkg", DoD: "go test PASS", Depends: "111.1", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
		{TaskID: "111.3", Title: "CLI verbs", DoD: "verb schemas PASS", Depends: "111.2", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "plan_state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Status: DB absent = not-configured tri-state, silent (no error, no warning).
func TestStatus_NotConfigured(t *testing.T) {
	st, err := Status(filepath.Join(t.TempDir(), "absent", "plan_state.db"))
	if err != nil {
		t.Fatalf("Status must not error on absent DB: %v", err)
	}
	if !st.Healthy || st.Reason != "not-configured" {
		t.Fatalf("want healthy=true reason=not-configured, got healthy=%v reason=%q", st.Healthy, st.Reason)
	}
}

func TestReindex_Idempotent(t *testing.T) {
	s := openTemp(t)
	if err := s.Reindex("default", sampleTasks()); err != nil {
		t.Fatalf("Reindex 1: %v", err)
	}
	if err := s.Reindex("default", sampleTasks()); err != nil {
		t.Fatalf("Reindex 2: %v", err)
	}
	nodes, err := s.Nodes("default")
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("want 3 nodes after double reindex, got %d", len(nodes))
	}
	// marker mapping
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if byID["111.1"].Marker != "done" {
		t.Fatalf("111.1 marker: want done, got %q", byID["111.1"].Marker)
	}
	if byID["111.2"].Marker != "todo" {
		t.Fatalf("111.2 marker: want todo, got %q", byID["111.2"].Marker)
	}
	// [P] flag
	if !byID["111.2"].Parallel {
		t.Fatalf("111.2 should be parallel ([P] in title)")
	}
	if byID["111.3"].Parallel {
		t.Fatalf("111.3 should not be parallel")
	}
	// deps table
	deps, err := s.Deps("default")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if len(deps["111.2"]) != 1 || deps["111.2"][0] != "111.1" {
		t.Fatalf("111.2 deps: want [111.1], got %v", deps["111.2"])
	}
	if len(deps["111.1"]) != 0 {
		t.Fatalf("111.1 deps: want none (Depends '-'), got %v", deps["111.1"])
	}
}

// Reindex removes rows whose task disappeared from Plans.md.
func TestReindex_RemovesStaleRows(t *testing.T) {
	s := openTemp(t)
	if err := s.Reindex("default", sampleTasks()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if err := s.Reindex("default", sampleTasks()[:2]); err != nil {
		t.Fatalf("Reindex shrink: %v", err)
	}
	nodes, _ := s.Nodes("default")
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes after shrink reindex, got %d", len(nodes))
	}
}

func TestDrift_DetectsChangedRow(t *testing.T) {
	s := openTemp(t)
	tasks := sampleTasks()
	if err := s.Reindex("default", tasks); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	// no drift when identical
	drift, err := s.Drift("default", tasks)
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	if len(drift) != 0 {
		t.Fatalf("want no drift, got %v", drift)
	}
	// mutate one row (status change in Plans.md not yet reindexed)
	tasks[1].Status = "cc:wip"
	tasks[1].Tags = plans.Tags{Wip: true}
	drift, err = s.Drift("default", tasks)
	if err != nil {
		t.Fatalf("Drift 2: %v", err)
	}
	if len(drift) != 1 || drift[0].ID != "111.2" {
		t.Fatalf("want drift on 111.2, got %v", drift)
	}
}

// Deleting the DB loses no truth: reindex rebuilds all definitional rows.
func TestRebuild_AfterDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan_state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Reindex("default", sampleTasks()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	_ = s.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.Reindex("default", sampleTasks()); err != nil {
		t.Fatalf("Reindex after delete: %v", err)
	}
	nodes, _ := s2.Nodes("default")
	if len(nodes) != 3 {
		t.Fatalf("want 3 nodes rebuilt, got %d", len(nodes))
	}
}

func TestStatus_Healthy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan_state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()
	st, err := Status(path)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Healthy || st.Reason != "" {
		t.Fatalf("want healthy=true reason=\"\", got healthy=%v reason=%q", st.Healthy, st.Reason)
	}
}

func TestStatus_Corrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan_state.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	st, err := Status(path)
	if err != nil {
		t.Fatalf("Status must not hard-error on corrupted DB: %v", err)
	}
	if st.Healthy || st.Reason != "corrupted" {
		t.Fatalf("want healthy=false reason=corrupted, got healthy=%v reason=%q", st.Healthy, st.Reason)
	}
}

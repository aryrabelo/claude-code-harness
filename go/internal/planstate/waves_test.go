package planstate

import (
	"testing"

	"github.com/Chachamaru127/claude-code-harness/go/internal/plans"
)

func waveTasks() []plans.Task {
	return []plans.Task{
		{TaskID: "1.1", Title: "a", Depends: "-", Status: "cc:done", Tags: plans.Tags{Done: true}},
		{TaskID: "1.2", Title: "b `[P]`", Depends: "1.1", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
		{TaskID: "1.3", Title: "c `[P]`", Depends: "1.1", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
		{TaskID: "1.4", Title: "d", Depends: "1.2, 1.3", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
		{TaskID: "1.5", Title: "e blocked", Depends: "-", Status: "blocked", Tags: plans.Tags{Blocked: true}},
	}
}

func seeded(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	if err := s.Reindex("default", waveTasks()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	return s
}

// Waves: done tasks excluded; blocked excluded; layers follow the DAG.
func TestWaves_TopoLayers(t *testing.T) {
	s := seeded(t)
	waves, err := s.Waves("default")
	if err != nil {
		t.Fatalf("Waves: %v", err)
	}
	if len(waves) != 2 {
		t.Fatalf("want 2 waves, got %d: %v", len(waves), waves)
	}
	if len(waves[0]) != 2 || waves[0][0].ID != "1.2" || waves[0][1].ID != "1.3" {
		t.Fatalf("wave 0: want [1.2 1.3], got %v", waves[0])
	}
	if len(waves[1]) != 1 || waves[1][0].ID != "1.4" {
		t.Fatalf("wave 1: want [1.4], got %v", waves[1])
	}
}

// Next: first unblocked todo/wip task whose deps are all done.
func TestNext_FirstUnblocked(t *testing.T) {
	s := seeded(t)
	n, err := s.Next("default")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if n == nil || n.ID != "1.2" {
		t.Fatalf("want next=1.2, got %v", n)
	}
}

// Next returns nil when nothing is ready (all done).
func TestNext_NoneReady(t *testing.T) {
	s := openTemp(t)
	done := []plans.Task{{TaskID: "1.1", Depends: "-", Status: "cc:done", Tags: plans.Tags{Done: true}}}
	if err := s.Reindex("default", done); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	n, err := s.Next("default")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if n != nil {
		t.Fatalf("want nil, got %v", n)
	}
}

// A dependency cycle must not hang or panic; cyclic tasks are simply never ready.
func TestWaves_CycleSafe(t *testing.T) {
	s := openTemp(t)
	cyc := []plans.Task{
		{TaskID: "2.1", Depends: "2.2", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
		{TaskID: "2.2", Depends: "2.1", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
		{TaskID: "2.3", Depends: "-", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
	}
	if err := s.Reindex("default", cyc); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	waves, err := s.Waves("default")
	if err != nil {
		t.Fatalf("Waves: %v", err)
	}
	if len(waves) != 1 || len(waves[0]) != 1 || waves[0][0].ID != "2.3" {
		t.Fatalf("want single wave [2.3] (cycle excluded), got %v", waves)
	}
}

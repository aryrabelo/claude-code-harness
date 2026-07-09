package planstate

import (
	"sync"
	"testing"
	"time"

	"github.com/Chachamaru127/claude-code-harness/go/internal/plans"
)

func runsStore(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	tasks := []plans.Task{
		{TaskID: "3.1", Depends: "-", Status: "cc:todo", Tags: plans.Tags{Todo: true}},
	}
	if err := s.Reindex("default", tasks); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	return s
}

// Exactly one of two concurrent claimers must win (single-statement CAS).
func TestClaim_ConcurrentSingleWinner(t *testing.T) {
	s := runsStore(t)
	var wg sync.WaitGroup
	wins := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			runID, err := s.Claim("default", "3.1", ClaimOpts{PID: pid, SessionID: "s", Assignee: "test", StaleAfter: time.Hour})
			if err != nil {
				t.Errorf("Claim: %v", err)
				return
			}
			if runID != 0 {
				wins <- runID
			}
		}(1000 + i)
	}
	wg.Wait()
	close(wins)
	var n int
	for range wins {
		n++
	}
	if n != 1 {
		t.Fatalf("want exactly 1 winner, got %d", n)
	}
}

// A live claim blocks re-claim; a stale claim (old heartbeat) is taken over.
func TestClaim_StaleTakeover(t *testing.T) {
	s := runsStore(t)
	id1, err := s.Claim("default", "3.1", ClaimOpts{PID: 1, SessionID: "a", Assignee: "x", StaleAfter: time.Hour})
	if err != nil || id1 == 0 {
		t.Fatalf("first claim: id=%d err=%v", id1, err)
	}
	// live → refused
	id2, err := s.Claim("default", "3.1", ClaimOpts{PID: 2, SessionID: "b", Assignee: "y", StaleAfter: time.Hour})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if id2 != 0 {
		t.Fatalf("live claim must not be taken over")
	}
	// make it stale
	if _, err := s.db.Exec(`UPDATE plan_runs SET heartbeat_at=? WHERE id=?`, time.Now().Add(-3*time.Hour).Unix(), id1); err != nil {
		t.Fatal(err)
	}
	id3, err := s.Claim("default", "3.1", ClaimOpts{PID: 3, SessionID: "c", Assignee: "z", StaleAfter: time.Hour})
	if err != nil {
		t.Fatalf("stale takeover: %v", err)
	}
	if id3 == 0 {
		t.Fatalf("stale claim must be taken over")
	}
	// old run marked crashed
	var outcome string
	if err := s.db.QueryRow(`SELECT outcome FROM plan_runs WHERE id=?`, id1).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "crashed" {
		t.Fatalf("old run outcome: want crashed, got %q", outcome)
	}
}

func TestHeartbeatAndEnd(t *testing.T) {
	s := runsStore(t)
	id, _ := s.Claim("default", "3.1", ClaimOpts{PID: 1, SessionID: "a", Assignee: "x", StaleAfter: time.Hour})
	if err := s.Heartbeat(id); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := s.EndRun(id, "done", "evidence: commit abc"); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	var outcome, log string
	if err := s.db.QueryRow(`SELECT outcome,run_log FROM plan_runs WHERE id=?`, id).Scan(&outcome, &log); err != nil {
		t.Fatal(err)
	}
	if outcome != "done" || log == "" {
		t.Fatalf("want done+log, got %q %q", outcome, log)
	}
	// ended run does not block a new claim
	id2, err := s.Claim("default", "3.1", ClaimOpts{PID: 9, SessionID: "n", Assignee: "x", StaleAfter: time.Hour})
	if err != nil || id2 == 0 {
		t.Fatalf("re-claim after end: id=%d err=%v", id2, err)
	}
}

// Reap marks dead-pid runs on this machine as crashed.
func TestReap_DeadPid(t *testing.T) {
	s := runsStore(t)
	// PID 1 is launchd (alive, not ours) — use an impossible PID instead.
	deadPID := 4194304*4 + 1234567 // far beyond pid_max on macOS/linux
	id, _ := s.Claim("default", "3.1", ClaimOpts{PID: deadPID, SessionID: "a", Assignee: "x", StaleAfter: time.Hour})
	if id == 0 {
		t.Fatal("claim failed")
	}
	n, err := s.Reap("default", 2*time.Hour)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 reaped, got %d", n)
	}
	var outcome string
	_ = s.db.QueryRow(`SELECT outcome FROM plan_runs WHERE id=?`, id).Scan(&outcome)
	if outcome != "crashed" {
		t.Fatalf("want crashed, got %q", outcome)
	}
}

// Resume lists running rows whose pid is dead or heartbeat stale.
func TestResume_ListsStaleRunning(t *testing.T) {
	s := runsStore(t)
	id, _ := s.Claim("default", "3.1", ClaimOpts{PID: 4194304*4 + 999999, SessionID: "a", Assignee: "x", StaleAfter: time.Hour})
	if id == 0 {
		t.Fatal("claim failed")
	}
	rows, err := s.Resumable("default", time.Hour)
	if err != nil {
		t.Fatalf("Resumable: %v", err)
	}
	if len(rows) != 1 || rows[0].NodeID != "3.1" {
		t.Fatalf("want [3.1], got %v", rows)
	}
}

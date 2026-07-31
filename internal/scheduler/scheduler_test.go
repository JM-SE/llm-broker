package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

func mustClaim(t *testing.T, s *Scheduler) postgres.TaskWithPayload {
	t.Helper()
	res, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if res.NoTask {
		t.Fatal("expected a claimable task")
	}
	return res.Task
}

func TestClaimOrderByPriorityClass(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "order-client")
	batch := mustPolicy(t, "order-batch", "batch", 3)
	normal := mustPolicy(t, "order-normal", "normal", 3)
	high := mustPolicy(t, "order-high", "high", 3)
	mustGrant(t, client.ID, batch.ID)
	mustGrant(t, client.ID, normal.ID)
	mustGrant(t, client.ID, high.ID)
	mustEnqueue(t, client.ID, batch.ID, "instruct", uniqueName("k"))
	mustEnqueue(t, client.ID, normal.ID, "instruct", uniqueName("k"))
	mustEnqueue(t, client.ID, high.ID, "instruct", uniqueName("k"))

	_, s := newScheduler(t)
	first := mustClaim(t, s)
	second := mustClaim(t, s)
	third := mustClaim(t, s)
	if first.WorkloadID != high.ID {
		t.Errorf("first claim workload = %d, want high (%d)", first.WorkloadID, high.ID)
	}
	if second.WorkloadID != normal.ID {
		t.Errorf("second claim workload = %d, want normal (%d)", second.WorkloadID, normal.ID)
	}
	if third.WorkloadID != batch.ID {
		t.Errorf("third claim workload = %d, want batch (%d)", third.WorkloadID, batch.ID)
	}
}

func TestAgingRaiseAndCaps(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "aging-client")
	batch := mustPolicy(t, "aging-batch", "batch", 3)
	normal := mustPolicy(t, "aging-normal", "normal", 3)
	high := mustPolicy(t, "aging-high", "high", 3)
	mustGrant(t, client.ID, batch.ID)
	mustGrant(t, client.ID, normal.ID)
	mustGrant(t, client.ID, high.ID)

	// Old batch task (30 min queued): 100 + 300 = 400, above fresh high.
	oldBatch := mustEnqueue(t, client.ID, batch.ID, "instruct", uniqueName("k"))
	backdateTask(t, oldBatch, 1800)
	// Fresh high: 300.
	mustEnqueue(t, client.ID, high.ID, "instruct", uniqueName("k"))
	// Fresh normal: 200.
	mustEnqueue(t, client.ID, normal.ID, "instruct", uniqueName("k"))

	_, s := newScheduler(t)
	got := []postgres.TaskWithPayload{mustClaim(t, s), mustClaim(t, s), mustClaim(t, s)}
	if got[0].ID != oldBatch {
		t.Errorf("first claim = %s, want aged batch %s", got[0].ID, oldBatch)
	}
	if got[0].WorkloadID != batch.ID {
		t.Errorf("first claim workload = %d, want batch", got[0].WorkloadID)
	}
	if got[1].WorkloadID != high.ID {
		t.Errorf("second claim workload = %d, want high", got[1].WorkloadID)
	}
	if got[2].WorkloadID != normal.ID {
		t.Errorf("third claim workload = %d, want normal", got[2].WorkloadID)
	}
}

func TestTieBreakByCreatedAtThenID(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "tie-client")
	policy := mustPolicy(t, "tie-policy", "normal", 3)
	mustGrant(t, client.ID, policy.ID)
	first := mustEnqueue(t, client.ID, policy.ID, "instruct", uniqueName("k"))
	second := mustEnqueue(t, client.ID, policy.ID, "instruct", uniqueName("k"))

	// Force identical creation times: ordering falls to the task ID.
	ts := time.Now().UTC()
	for _, id := range []interface{}{first, second} {
		if _, err := adminStore.Pool().Exec(context.Background(),
			"UPDATE tasks SET created_at = $2 WHERE id = $1", id, ts); err != nil {
			t.Fatalf("pin created_at: %v", err)
		}
	}
	want := first.String()
	if second.String() < want {
		want = second.String()
	}

	_, s := newScheduler(t)
	got := mustClaim(t, s)
	if got.ID.String() != want {
		t.Errorf("first claim = %s, want lexicographically smallest id %s", got.ID, want)
	}
}

func TestExhaustedAttemptsExcluded(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "attempts-client")
	policy := mustPolicy(t, "attempts-policy", "normal", 2)
	mustGrant(t, client.ID, policy.ID)
	id := mustEnqueue(t, client.ID, policy.ID, "instruct", uniqueName("k"))
	if _, err := adminStore.Pool().Exec(context.Background(),
		"UPDATE tasks SET attempts = 2 WHERE id = $1", id); err != nil {
		t.Fatalf("set attempts: %v", err)
	}

	_, s := newScheduler(t)
	res, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !res.NoTask {
		t.Fatalf("claimed exhausted task %s", res.Task.ID)
	}
}

func TestNotGrantedExcluded(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "grant-client")
	policy := mustPolicy(t, "grant-policy", "normal", 3)
	// No grant: the task must not be eligible.
	mustEnqueue(t, client.ID, policy.ID, "instruct", uniqueName("k"))

	_, s := newScheduler(t)
	res, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !res.NoTask {
		t.Fatalf("claimed a task whose client has no grant: %s", res.Task.ID)
	}
}

func TestConcurrentClaimersClaimEachOnce(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "concurrent-client")
	policy := mustPolicy(t, "concurrent-policy", "normal", 3)
	mustGrant(t, client.ID, policy.ID)
	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		id := mustEnqueue(t, client.ID, policy.ID, "instruct", uniqueName("k"))
		ids[id.String()] = true
	}

	_, s := newScheduler(t)
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				res, err := s.ClaimNext(context.Background())
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if res.NoTask {
					return
				}
				mu.Lock()
				claimed[res.Task.ID.String()]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != 5 {
		t.Errorf("claimed %d distinct tasks, want 5", len(claimed))
	}
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("task %s claimed %d times, want 1", id, n)
		}
		if !ids[id] {
			t.Errorf("claimed unknown task %s", id)
		}
	}
}

func TestGlobalSerialAcrossCapabilities(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "serial-client")
	policyX := mustPolicy(t, "serial-x", "normal", 3)
	policyY := mustPolicy(t, "serial-y", "normal", 3)
	mustGrant(t, client.ID, policyX.ID)
	mustGrant(t, client.ID, policyY.ID)
	x := mustEnqueue(t, client.ID, policyX.ID, "cap-x", uniqueName("k"))
	y := mustEnqueue(t, client.ID, policyY.ID, "cap-y", uniqueName("k"))

	_, s := newScheduler(t)
	first := mustClaim(t, s)
	second := mustClaim(t, s)
	// One inference at a time: the second claim is the other task, and a
	// running task is never re-claimed.
	if first.ID == second.ID {
		t.Fatal("same task claimed twice")
	}
	if first.ID != x && first.ID != y {
		t.Errorf("first claim %s is not one of the fixtures", first.ID)
	}
	// With both now running, no further claim is possible.
	res, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !res.NoTask {
		t.Errorf("claimed %s while two tasks already run", res.Task.ID)
	}
}

func TestBatchAffinityPreferenceAndClose(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "batch-client")
	btch := mustPolicy(t, "batch-af", "batch", 3)
	high := mustPolicy(t, "batch-hi", "high", 3)
	mustGrant(t, client.ID, btch.ID)
	mustGrant(t, client.ID, high.ID)
	for i := 0; i < 3; i++ {
		mustEnqueue(t, client.ID, btch.ID, "instruct", uniqueName("k"))
	}

	_, s := newScheduler(t)
	res1, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res1.Decision.Reason != "batch_opened" || res1.Decision.BatchKey != "instruct" {
		t.Errorf("claim 1 decision = %+v, want opened instruct batch", res1.Decision)
	}
	res2, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res2.Decision.Reason != "batch_continued" {
		t.Errorf("claim 2 decision = %+v, want continued", res2.Decision)
	}

	// A fresh high-priority task of another capability arrives: the batch is
	// a preference; the next claim is the high task and the batch reopens.
	mustEnqueue(t, client.ID, high.ID, "thinking", uniqueName("k"))
	res3, err := s.ClaimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res3.Task.Capability != "thinking" {
		t.Errorf("claim 3 capability = %q, want thinking", res3.Task.Capability)
	}
	if res3.Decision.Reason != "batch_opened" || res3.Decision.BatchKey != "thinking" {
		t.Errorf("claim 3 decision = %+v, want reopened for thinking", res3.Decision)
	}
}

// TestConformanceSQLVsDomain verifies the SQL effective-priority expression
// and the pure domain function agree across classes and queued ages,
// including per-class caps. Each claimed priority comes from SQL (recorded
// in the claim decision); each expected value comes from the domain
// function.
func TestConformanceSQLVsDomain(t *testing.T) {
	clearQueued(t)
	client := mustClient(t, "conf-client")
	classes := []string{"high", "normal", "batch"}
	ages := []int64{0, 600, 1800, 3600, 7200}
	type fixture struct {
		id    string
		class string
		age   int64
	}
	var fixtures []fixture
	for _, class := range classes {
		policy := mustPolicy(t, "conf-"+class, class, 3)
		mustGrant(t, client.ID, policy.ID)
		for _, age := range ages {
			id := mustEnqueue(t, client.ID, policy.ID, "instruct", uniqueName("k"))
			if age > 0 {
				backdateTask(t, id, age)
			}
			fixtures = append(fixtures, fixture{id: id.String(), class: class, age: age})
		}
	}

	_, s := newScheduler(t)
	sqlPriority := map[string]int{}
	for i := 0; i < len(fixtures); i++ {
		res, err := s.ClaimNext(context.Background())
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if res.NoTask {
			t.Fatalf("claim %d: no task (claimed %d of %d)", i, len(sqlPriority), len(fixtures))
		}
		sqlPriority[res.Task.ID.String()] = res.Decision.EffectivePriority
	}

	if len(sqlPriority) != len(fixtures) {
		t.Fatalf("claimed %d distinct tasks, want %d", len(sqlPriority), len(fixtures))
	}
	for _, f := range fixtures {
		pc, err := domain.ParsePriorityClass(f.class)
		if err != nil {
			t.Fatal(err)
		}
		want := domain.EffectivePriority(pc, f.age)
		got, ok := sqlPriority[f.id]
		if !ok {
			t.Fatalf("task %s was never claimed", f.id)
		}
		if got != want {
			t.Errorf("task %s (%s, %ds queued): SQL priority %d, domain priority %d",
				f.id, f.class, f.age, got, want)
		}
	}
}

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mustClaimTask claims queued tasks until the wanted task is claimed. The
// queue is shared across tests, so the oldest-first claim may grab another
// test's task first.
func mustClaimTask(t *testing.T, taskID uuid.UUID) {
	t.Helper()
	for {
		tw, err := adminStore.ClaimNextQueued(context.Background(), uuid.New(), 60*time.Second)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if tw.ID == taskID {
			return
		}
	}
}

func TestSaveResultAndRead(t *testing.T) {
	ctx := context.Background()
	c := mustCreateClient(t, adminStore, "result-client")
	p := mustUpsertPolicy(t, adminStore, "result-workload")
	res := mustCreateTask(t, adminStore, c.ID, p.ID, "result-key-01", testHash('r'))
	mustClaimTask(t, res.TaskID)

	if err := adminStore.TransitionTask(ctx, res.TaskID, "running", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := adminStore.SaveResult(ctx, res.TaskID, "hello", "stop", 10, 5, 15); err != nil {
		t.Fatalf("save result: %v", err)
	}
	got, err := adminStore.GetResultByTaskID(ctx, res.TaskID)
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.Content != "hello" || got.FinishReason != "stop" ||
		got.PromptTokens != 10 || got.CompletionTokens != 5 || got.TotalTokens != 15 {
		t.Errorf("result mismatch: %+v", got)
	}
	if _, err := adminStore.GetResultByTaskID(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing result: want ErrNotFound, got %v", err)
	}
}

func TestMarkAttemptFailed(t *testing.T) {
	ctx := context.Background()
	c := mustCreateClient(t, adminStore, "attempt-client")
	p := mustUpsertPolicy(t, adminStore, "attempt-workload")
	res := mustCreateTask(t, adminStore, c.ID, p.ID, "attempt-key-01", testHash('a'))
	mustClaimTask(t, res.TaskID)

	if err := adminStore.MarkAttemptFailed(ctx, res.TaskID, 1, "output_invalid", "output_invalid"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	att, err := adminStore.GetLastAttempt(ctx, res.TaskID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if att.State != "failed" || att.ErrorClass == nil || *att.ErrorClass != "output_invalid" ||
		att.ErrorCode == nil || *att.ErrorCode != "output_invalid" {
		t.Errorf("attempt mismatch: %+v", att)
	}
	if att.FinishedAt == nil || time.Since(*att.FinishedAt) > time.Minute {
		t.Errorf("finished_at not set: %+v", att)
	}
	if err := adminStore.MarkAttemptFailed(ctx, res.TaskID, 99, "x", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing attempt: want ErrNotFound, got %v", err)
	}
}

func TestCountActiveTasks(t *testing.T) {
	ctx := context.Background()
	c := mustCreateClient(t, adminStore, "active-client")
	p := mustUpsertPolicy(t, adminStore, "active-workload")
	mustCreateTask(t, adminStore, c.ID, p.ID, "active-key-01", testHash('1'))
	mustCreateTask(t, adminStore, c.ID, p.ID, "active-key-02", testHash('2'))
	n, err := adminStore.CountActiveTasks(ctx, c.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("active = %d, want 2", n)
	}
	mustClaim(t, adminStore, uuid.New())
	n, err = adminStore.CountActiveTasks(ctx, c.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // one running + one queued: both non-terminal
		t.Errorf("active after claim = %d, want 2", n)
	}
}

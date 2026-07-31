package domain

import (
	"errors"
	"testing"
)

func TestTaskStateTransitions(t *testing.T) {
	// Expected matrix from the master contract.
	allowed := map[TaskState][]TaskState{
		TaskQueued:    {TaskRunning, TaskCancelled},
		TaskRunning:   {TaskQueued, TaskSucceeded, TaskFailed, TaskCancelled},
		TaskSucceeded: {},
		TaskFailed:    {},
		TaskCancelled: {},
	}
	for from, tos := range allowed {
		for _, to := range []TaskState{TaskQueued, TaskRunning, TaskSucceeded, TaskFailed, TaskCancelled} {
			want := false
			for _, allowedTo := range tos {
				if to == allowedTo {
					want = true
					break
				}
			}
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTaskTransitionAppliesAndRejects(t *testing.T) {
	task := Task{State: TaskQueued}
	if err := task.Transition(TaskRunning); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	if task.State != TaskRunning {
		t.Fatalf("state = %s, want running", task.State)
	}
	// Terminal must never transition.
	if err := task.Transition(TaskSucceeded); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	if err := task.Transition(TaskFailed); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition error = %v, want ErrInvalidTransition", err)
	}
	if task.State != TaskSucceeded {
		t.Fatalf("state mutated on invalid transition: %s", task.State)
	}
}

func TestParseTaskState(t *testing.T) {
	for _, s := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		if _, err := ParseTaskState(s); err != nil {
			t.Errorf("ParseTaskState(%q): %v", s, err)
		}
	}
	if _, err := ParseTaskState("pending"); err == nil {
		t.Error("ParseTaskState(pending) should fail")
	}
}

func TestTerminal(t *testing.T) {
	for s, want := range map[TaskState]bool{
		TaskQueued: false, TaskRunning: false,
		TaskSucceeded: true, TaskFailed: true, TaskCancelled: true,
	} {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}

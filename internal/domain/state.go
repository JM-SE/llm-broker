package domain

import (
	"errors"
	"fmt"
)

// TaskState is the durable lifecycle state of a task. The set of states and
// their transitions are a frozen product contract.
type TaskState string

const (
	TaskQueued    TaskState = "queued"
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
	TaskCancelled TaskState = "cancelled"
)

var validStates = map[TaskState]bool{
	TaskQueued:    true,
	TaskRunning:   true,
	TaskSucceeded: true,
	TaskFailed:    true,
	TaskCancelled: true,
}

// ParseTaskState converts a string to a TaskState, or errors.
func ParseTaskState(s string) (TaskState, error) {
	ts := TaskState(s)
	if !validStates[ts] {
		return "", fmt.Errorf("unknown task state %q", s)
	}
	return ts, nil
}

// Terminal reports whether the state is a terminal state. Terminal states
// never transition.
func (s TaskState) Terminal() bool {
	switch s {
	case TaskSucceeded, TaskFailed, TaskCancelled:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether a direct transition from s to next is
// allowed by the product contract:
//
//	queued    -> running | cancelled
//	running   -> queued (only proven-safe retry) | succeeded | failed
//	           | cancelled (inference completes, output discarded)
//	terminal  -> (never)
func (s TaskState) CanTransitionTo(next TaskState) bool {
	if s.Terminal() {
		return false
	}
	switch s {
	case TaskQueued:
		return next == TaskRunning || next == TaskCancelled
	case TaskRunning:
		return next == TaskQueued || next == TaskSucceeded || next == TaskFailed || next == TaskCancelled
	default:
		return false
	}
}

// ErrInvalidTransition is returned by Task.Transition for disallowed moves.
var ErrInvalidTransition = errors.New("invalid task state transition")

// Task is the core durable task record. Persistence of the full task row is
// owned by later phases; the struct only carries the lifecycle contract.
type Task struct {
	ID          string
	ClientID    string
	Workload    string
	Capability  string
	State       TaskState
	Attempts    int
	CreatedAt   int64  // unix nanoseconds
	UpdatedAt   int64  // unix nanoseconds
	RequestHash string // canonical SHA-256 of the normalized request
}

// Transition validates and applies a state transition.
func (t *Task) Transition(next TaskState) error {
	if !t.State.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.State, next)
	}
	t.State = next
	return nil
}

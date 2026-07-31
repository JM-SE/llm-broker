// Package scheduler implements the broker-owned scheduling policy: effective
// priority with aging, deterministic tie-breaking, bounded affinity batches,
// global leadership, and the single atomic claim path. Clients never
// influence ordering; every decision is derived from operator configuration
// and durable task state.
package scheduler

import (
	"errors"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// Policy holds the scheduler knobs. Aging parameters are shared with the
// domain package (single definition of the policy).
type Policy struct {
	// Lease is the claim duration of a running task; the worker refreshes
	// it while executing.
	Lease time.Duration
	// BatchSize closes an affinity batch after this many consecutive
	// claims of the same capability.
	BatchSize int
	// BatchMaxAge closes an affinity batch after this much time.
	BatchMaxAge time.Duration
	// HeartbeatInterval refreshes the leadership heartbeat.
	HeartbeatInterval time.Duration
}

// DefaultPolicy returns the v1 product defaults: 60s task lease, affinity
// batches of four tasks or ten minutes, and a ten-second heartbeat.
func DefaultPolicy() Policy {
	return Policy{
		Lease:             60 * time.Second,
		BatchSize:         4,
		BatchMaxAge:       10 * time.Minute,
		HeartbeatInterval: 10 * time.Second,
	}
}

// ErrNotLeader reports a claim attempt without current leadership. Standby
// workers never claim.
var ErrNotLeader = errors.New("scheduler: no current leadership; refusing to claim")

// EffectivePriority resolves the broker-owned effective priority of a task
// queued for the given duration. It delegates to the domain function so the
// pure policy and the SQL ordering share one definition; conformance is
// verified by tests.
func EffectivePriority(class domain.PriorityClass, queuedFor time.Duration) int {
	return domain.EffectivePriority(class, int64(queuedFor.Seconds()))
}

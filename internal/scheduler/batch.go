package scheduler

import "time"

// AffinityState tracks the open affinity batch: consecutive claims of the
// same capability are preferred up to BatchSize or BatchMaxAge. A batch is a
// preference, not an entitlement: the scheduler always claims the globally
// best eligible task, so a task with higher effective priority of a
// different capability naturally closes the batch before the next claim.
type AffinityState struct {
	Key      string
	OpenedAt time.Time
	Claimed  int
	Open     bool
}

// Decision describes one claim decision for sanitized diagnostics: capability
// slugs, priorities, and batch counters only. Never client, task, or prompt
// labels.
type Decision struct {
	Claimed           bool   `json:"claimed"`
	Capability        string `json:"capability,omitempty"`
	EffectivePriority int    `json:"effective_priority,omitempty"`
	BatchOpen         bool   `json:"batch_open"`
	BatchKey          string `json:"batch_key,omitempty"`
	BatchClaimed      int    `json:"batch_claimed,omitempty"`
	Reason            string `json:"reason"`
}

// Consider updates the affinity state after one successful claim and returns
// the diagnostic decision. A claim continues the open batch only when it has
// the same capability, the size bound is not reached, and the age bound
// holds; otherwise the previous batch closes and a new one opens.
func (a *AffinityState) Consider(capability string, effective, batchSize int, batchMaxAge time.Duration, now time.Time) Decision {
	continues := a.Open && a.Key == capability && a.Claimed < batchSize &&
		now.Sub(a.OpenedAt) <= batchMaxAge
	if !continues {
		a.Open = true
		a.Key = capability
		a.OpenedAt = now
		a.Claimed = 1
		return Decision{
			Claimed: true, Capability: capability, EffectivePriority: effective,
			BatchOpen: true, BatchKey: capability, BatchClaimed: 1,
			Reason: "batch_opened",
		}
	}
	a.Claimed++
	return Decision{
		Claimed: true, Capability: capability, EffectivePriority: effective,
		BatchOpen: true, BatchKey: a.Key, BatchClaimed: a.Claimed,
		Reason: "batch_continued",
	}
}

// Close ends the affinity batch (queue drained or leadership lost).
func (a *AffinityState) Close() {
	a.Open = false
	a.Key = ""
	a.OpenedAt = time.Time{}
	a.Claimed = 0
}

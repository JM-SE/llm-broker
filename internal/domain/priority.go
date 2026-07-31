package domain

import "fmt"

// PriorityClass is the operator-assigned scheduling class of a workload.
// Client requests never carry a priority; the broker derives it from the
// workload policy.
type PriorityClass string

const (
	// PriorityHigh has base effective priority 300.
	PriorityHigh PriorityClass = "high"
	// PriorityNormal has base effective priority 200.
	PriorityNormal PriorityClass = "normal"
	// PriorityBatch has base effective priority 100.
	PriorityBatch PriorityClass = "batch"
)

// Base returns the base effective priority of the class.
func (p PriorityClass) Base() int {
	switch p {
	case PriorityHigh:
		return 300
	case PriorityNormal:
		return 200
	case PriorityBatch:
		return 100
	default:
		return 0
	}
}

// Valid reports whether the class is one of the three stable classes.
func (p PriorityClass) Valid() bool {
	switch p {
	case PriorityHigh, PriorityNormal, PriorityBatch:
		return true
	default:
		return false
	}
}

// Aging parameters: queued tasks gain +100 effective priority per
// AgingInterval, capped at AgingCap. Constants shared with the scheduler.
const (
	AgingStep   = 100
	AgingCap    = 300
	AgingPer    = 10 * 60 // seconds: one step per 10 queued minutes
	AgingCapSec = AgingCap
)

// AgingBoost computes the priority boost for a task queued for d seconds.
// The boost is capped so aged batch work can reach normal priority but
// never overtakes the operator's high class.
func AgingBoost(queuedSeconds int64) int {
	if queuedSeconds <= 0 {
		return 0
	}
	steps := int(queuedSeconds / AgingPer)
	boost := steps * AgingStep
	if boost > AgingCap {
		return AgingCap
	}
	if boost < 0 { // overflow guard
		return AgingCap
	}
	return boost
}

// EffectivePriority combines class base and aging boost. The boost is capped
// per class so that aged work can never overtake an operator-assigned higher
// class: batch may rise into normal territory, normal may approach but never
// exceed the high base, and high never ages (it is already the top class).
func EffectivePriority(class PriorityClass, queuedSeconds int64) int {
	base := class.Base()
	switch class {
	case PriorityBatch:
		return base + minInt(AgingBoost(queuedSeconds), AgingCap)
	case PriorityNormal:
		return base + minInt(AgingBoost(queuedSeconds), PriorityHigh.Base()-base)
	case PriorityHigh:
		return base
	default:
		return 0
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// String returns the class name.
func (p PriorityClass) String() string { return string(p) }

// ParsePriorityClass parses a priority class name.
func ParsePriorityClass(s string) (PriorityClass, error) {
	p := PriorityClass(s)
	if !p.Valid() {
		return "", fmt.Errorf("unknown priority class %q (want high, normal, or batch)", s)
	}
	return p, nil
}

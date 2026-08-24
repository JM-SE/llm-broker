package scheduler

import (
	"fmt"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// eligibleSelectSQL builds the policy-ordered eligible-task selection. The
// effective priority is computed in SQL from the exact constants the domain
// uses (class bases, aging step/interval, per-class caps) so the ordering
// cannot drift from the pure policy; a conformance test verifies SQL and
// pure policy agree. Eligibility requires queued state and attempts below the
// durable workload budget. Authorization is deliberately left to worker
// preflight so revocation cannot strand queued work.
func eligibleSelectSQL() string {
	high := int(domain.PriorityHigh.Base())
	normal := int(domain.PriorityNormal.Base())
	batch := int(domain.PriorityBatch.Base())
	normalCap := high - normal // aged normal never exceeds the high base
	return fmt.Sprintf(`
		WITH snapshot AS (SELECT now() AS ts),
		eligible AS (
			SELECT t.id, t.client_id, t.workload_id, w.name, t.capability, t.idempotency_key,
			       t.request_hash, t.state, t.attempts, t.lease_until, t.started_at,
			       t.finished_at, t.cancel_requested_at, t.retry_available_at,
			       t.created_at, t.updated_at,
			       p.payload,
			       CASE w.priority_class
			           WHEN 'high' THEN %d
			           WHEN 'normal' THEN %d + LEAST(GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (s.ts - t.created_at)) / %d)::int) * %d, %d)
			           WHEN 'batch' THEN %d + LEAST(GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (s.ts - t.created_at)) / %d)::int) * %d, %d)
			           ELSE 0
			       END AS effective_priority
			FROM tasks t
			JOIN task_payloads p ON p.task_id = t.id
			JOIN workload_policies w ON w.id = t.workload_id
			CROSS JOIN snapshot s
			WHERE t.state = 'queued'
			  AND t.attempts < w.max_attempts
			  AND (t.retry_available_at IS NULL OR t.retry_available_at <= s.ts)
			ORDER BY effective_priority DESC, t.created_at, t.id
			FOR UPDATE OF t SKIP LOCKED
			LIMIT 1
		)
		SELECT id, client_id, workload_id, name, capability, idempotency_key,
		       request_hash, state, attempts, lease_until, started_at,
		       finished_at, cancel_requested_at, retry_available_at,
		       created_at, updated_at,
		       payload, effective_priority
		FROM eligible
		`,
		high,
		normal, int(domain.AgingPer), int(domain.AgingStep), normalCap,
		batch, int(domain.AgingPer), int(domain.AgingStep), int(domain.AgingCap),
	)
}

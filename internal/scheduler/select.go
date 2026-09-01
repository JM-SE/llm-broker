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
// preflight so revocation cannot strand queued work. Arguments are an active
// affinity flag and its generic capability key.
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
			),
			max_priority AS (
				SELECT MAX(effective_priority) AS effective_priority FROM eligible
			),
			cohort AS (
				SELECT e.*
				FROM eligible e
				CROSS JOIN max_priority m
				WHERE e.effective_priority = m.effective_priority
			)
			SELECT e.id, e.client_id, e.workload_id, e.name, e.capability, e.idempotency_key,
			       e.request_hash, e.state, e.attempts, e.lease_until, e.started_at,
			       e.finished_at, e.cancel_requested_at, e.retry_available_at,
			       e.created_at, e.updated_at,
			       e.payload, e.effective_priority
			FROM cohort e
			JOIN tasks locked_task ON locked_task.id = e.id
			ORDER BY CASE WHEN $1::boolean AND e.capability = $2::text THEN 0 ELSE 1 END,
				e.created_at, e.id
			FOR UPDATE OF locked_task SKIP LOCKED
			LIMIT 1
		`,
		high,
		normal, int(domain.AgingPer), int(domain.AgingStep), normalCap,
		batch, int(domain.AgingPer), int(domain.AgingStep), int(domain.AgingCap),
	)
}

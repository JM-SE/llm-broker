package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/JM-SE/llm-broker/internal/policy"
)

// PolicyReport is a sanitized policy operation category. It contains no
// backend, model, credential, or private policy values.
type PolicyReport struct {
	Category string
}

// WorkloadPolicy is the durable policy projection used by runtime reads.
type WorkloadPolicy struct {
	ID                       int64
	Name                     string
	PriorityClass            string
	MaxActiveTasks           int
	MaxAttempts              int
	RetryDelaysSeconds       []int
	MaxOutputTokens          int
	MaxOutputBytes           int
	MaxOutputBytesExplicit   bool
	AllowedGeneration        []byte
	RequiredFeatures         []string
	WorkloadTimeoutSeconds   int
	PayloadRetentionSeconds  int64
	ResultRetentionSeconds   int64
	MetadataRetentionSeconds int64
	AllowedCapabilities      []string
	TopologyDigest           []byte
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// GetWorkloadPolicyByName returns the effective durable policy and normalized
// capability projection.
func (s *Store) GetWorkloadPolicyByName(ctx context.Context, name string) (WorkloadPolicy, error) {
	return s.getWorkloadPolicy(ctx, `SELECT id,name,priority_class,max_active_tasks,max_attempts,
		retry_delays_seconds,max_output_tokens,max_output_bytes,allowed_generation,required_features,
		workload_timeout_seconds,payload_retention_seconds,result_retention_seconds,
		metadata_retention_seconds,topology_digest,
		COALESCE((SELECT array_agg(capability ORDER BY capability) FROM workload_policy_capabilities wpc WHERE wpc.workload_id=workload_policies.id), '{}'::text[]),
		created_at,updated_at FROM workload_policies WHERE name=$1`, name)
}

// GetWorkloadPolicyByID returns the effective durable policy by numeric row ID.
func (s *Store) GetWorkloadPolicyByID(ctx context.Context, id int64) (WorkloadPolicy, error) {
	return s.getWorkloadPolicy(ctx, `SELECT id,name,priority_class,max_active_tasks,max_attempts,
		retry_delays_seconds,max_output_tokens,max_output_bytes,allowed_generation,required_features,
		workload_timeout_seconds,payload_retention_seconds,result_retention_seconds,
		metadata_retention_seconds,topology_digest,
		COALESCE((SELECT array_agg(capability ORDER BY capability) FROM workload_policy_capabilities wpc WHERE wpc.workload_id=workload_policies.id), '{}'::text[]),
		created_at,updated_at FROM workload_policies WHERE id=$1`, id)
}

func (s *Store) getWorkloadPolicy(ctx context.Context, query string, arg any) (WorkloadPolicy, error) {
	var p WorkloadPolicy
	var maxOutputBytes *int
	err := s.pool.QueryRow(ctx, query, arg).Scan(
		&p.ID, &p.Name, &p.PriorityClass, &p.MaxActiveTasks, &p.MaxAttempts,
		&p.RetryDelaysSeconds, &p.MaxOutputTokens, &maxOutputBytes, &p.AllowedGeneration,
		&p.RequiredFeatures, &p.WorkloadTimeoutSeconds, &p.PayloadRetentionSeconds,
		&p.ResultRetentionSeconds, &p.MetadataRetentionSeconds, &p.TopologyDigest,
		&p.AllowedCapabilities, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkloadPolicy{}, ErrNotFound
	}
	if maxOutputBytes != nil {
		p.MaxOutputBytes = *maxOutputBytes
		p.MaxOutputBytesExplicit = true
	}
	return p, err
}

// PolicyReportSummary is suitable for CLI output and stable assertions.
type PolicyReportSummary struct {
	Create           int
	Update           int
	Unchanged        int
	Unmanaged        int
	TopologyMismatch int
}

func (s *PolicyReportSummary) add(category string) {
	switch category {
	case "create":
		s.Create++
	case "update":
		s.Update++
	case "unchanged":
		s.Unchanged++
	case "unmanaged":
		s.Unmanaged++
	case "topology-mismatch":
		s.TopologyMismatch++
	}
}

// ApplyPolicies atomically applies all configured workload projections. It
// acquires the global policy mutex first and then immutable workload locks in
// lexical slug order. It never deletes unmanaged rows.
func (s *Store) ApplyPolicies(ctx context.Context, desired []policy.Workload) (PolicyReportSummary, error) {
	desired = sortedWorkloads(desired)
	var summary PolicyReportSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policy.GlobalPolicyApplyLockKey()); err != nil {
			return fmt.Errorf("acquire global policy lock: %w", err)
		}
		for _, workload := range desired {
			key, err := policy.PolicyLockKey(workload.Name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				return fmt.Errorf("acquire policy lock: %w", err)
			}
			category, err := applyPolicyProjection(ctx, tx, workload)
			if err != nil {
				return err
			}
			summary.add(category)
		}
		return nil
	})
	if err != nil {
		return PolicyReportSummary{}, err
	}
	return summary, nil
}

// DiffPolicies reads configured rows and reports semantic drift. It does not
// mutate policy or capability state.
func (s *Store) DiffPolicies(ctx context.Context, desired []policy.Workload) (PolicyReportSummary, error) {
	return s.inspectPolicies(ctx, desired, false)
}

// StatusPolicies reports configured coherence plus unmanaged rows. Missing,
// mutable-policy drift, and topology mismatch are blocking categories.
func (s *Store) StatusPolicies(ctx context.Context, desired []policy.Workload) (PolicyReportSummary, error) {
	return s.inspectPolicies(ctx, desired, true)
}

func (s *Store) inspectPolicies(ctx context.Context, desired []policy.Workload, status bool) (PolicyReportSummary, error) {
	desired = sortedWorkloads(desired)
	var summary PolicyReportSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policy.GlobalPolicyApplyLockKey()); err != nil {
			return fmt.Errorf("acquire global policy lock: %w", err)
		}
		seen := make(map[string]struct{}, len(desired))
		for _, workload := range desired {
			seen[workload.Name] = struct{}{}
			key, err := policy.PolicyLockKey(workload.Name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				return err
			}
			actual, exists, _, err := readPolicy(ctx, tx, workload.Name)
			if err != nil {
				return err
			}
			if !exists {
				summary.add("create")
				continue
			}
			if actual.TopologyDigest == [32]byte{} || actual.TopologyDigest != workload.TopologyDigest {
				summary.add("topology-mismatch")
				continue
			}
			if !actual.SemanticallyEqual(workload) {
				summary.add("update")
			} else {
				summary.add("unchanged")
			}
		}
		rows, err := tx.Query(ctx, `SELECT name FROM workload_policies ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			if _, ok := seen[name]; !ok {
				summary.add("unmanaged")
			}
		}
		return rows.Err()
	})
	if err != nil {
		return PolicyReportSummary{}, err
	}
	if !status {
		// Unmanaged-only state is informational and has no drift exit status.
	}
	return summary, nil
}

func sortedWorkloads(in []policy.Workload) []policy.Workload {
	out := append([]policy.Workload(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func applyPolicyProjection(ctx context.Context, tx pgx.Tx, desired policy.Workload) (string, error) {
	requiredFeatures := desired.RequiredFeatures
	if requiredFeatures == nil {
		// PostgreSQL stores this projection as NOT NULL. Treat an omitted
		// feature set as the empty set at the persistence boundary.
		requiredFeatures = []string{}
	}
	actual, exists, id, err := readPolicy(ctx, tx, desired.Name)
	if err != nil {
		return "", err
	}
	topologyChanged := exists && actual.TopologyDigest != desired.TopologyDigest
	if exists && actual.SemanticallyEqual(desired) {
		return "unchanged", nil
	}
	if !exists {
		err = tx.QueryRow(ctx, `
			INSERT INTO workload_policies (
				name, priority_class, max_active_tasks, max_attempts,
				retry_delays_seconds, max_output_tokens, max_output_bytes, allowed_generation,
				required_features, workload_timeout_seconds,
				payload_retention_seconds, result_retention_seconds,
				metadata_retention_seconds, topology_digest)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14)
			RETURNING id`, desired.Name, desired.PriorityClass, desired.MaxActiveTasks,
			desired.MaxAttempts, desired.RetryDelaysSeconds, desired.MaxOutputTokens, nullableOutputBytes(desired.MaxOutputBytes),
			desired.AllowedGeneration, requiredFeatures, desired.WorkloadTimeoutSeconds,
			desired.PayloadRetentionSeconds, desired.ResultRetentionSeconds,
			desired.MetadataRetentionSeconds, desired.TopologyDigest[:]).Scan(&id)
		if err != nil {
			return "", fmt.Errorf("insert workload policy: %w", err)
		}
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE workload_policies SET priority_class=$2, max_active_tasks=$3,
			max_attempts=$4, retry_delays_seconds=$5, max_output_tokens=$6,
			max_output_bytes=$7, allowed_generation=$8::jsonb, required_features=$9,
			workload_timeout_seconds=$10, payload_retention_seconds=$11,
			result_retention_seconds=$12, metadata_retention_seconds=$13,
			topology_digest=$14, updated_at=now() WHERE id=$1`, id,
			desired.PriorityClass, desired.MaxActiveTasks, desired.MaxAttempts,
			desired.RetryDelaysSeconds, desired.MaxOutputTokens, nullableOutputBytes(desired.MaxOutputBytes), desired.AllowedGeneration,
			requiredFeatures, desired.WorkloadTimeoutSeconds,
			desired.PayloadRetentionSeconds, desired.ResultRetentionSeconds,
			desired.MetadataRetentionSeconds, desired.TopologyDigest[:])
		if err != nil {
			return "", fmt.Errorf("update workload policy: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workload_policy_capabilities WHERE workload_id=$1`, id); err != nil {
		return "", fmt.Errorf("replace workload capabilities: %w", err)
	}
	for _, capability := range desired.AllowedCapabilities {
		if _, err := tx.Exec(ctx, `INSERT INTO workload_policy_capabilities (workload_id, capability) VALUES ($1,$2)`, id, capability); err != nil {
			return "", fmt.Errorf("insert workload capability: %w", err)
		}
	}
	if topologyChanged {
		return "topology-mismatch", nil
	}
	if exists {
		return "update", nil
	}
	return "create", nil
}

type policyQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readPolicy(ctx context.Context, q policyQuerier, name string) (policy.Workload, bool, int64, error) {
	var p policy.Workload
	var id int64
	var digest []byte
	var maxOutputBytes *int
	err := q.QueryRow(ctx, `SELECT id, name, priority_class, max_active_tasks,
		max_attempts, retry_delays_seconds, max_output_tokens, max_output_bytes, allowed_generation,
		required_features, workload_timeout_seconds, payload_retention_seconds,
		result_retention_seconds, metadata_retention_seconds, topology_digest
		FROM workload_policies WHERE name=$1`, name).Scan(
		&id, &p.Name, &p.PriorityClass, &p.MaxActiveTasks, &p.MaxAttempts,
		&p.RetryDelaysSeconds, &p.MaxOutputTokens, &maxOutputBytes, &p.AllowedGeneration,
		&p.RequiredFeatures, &p.WorkloadTimeoutSeconds, &p.PayloadRetentionSeconds,
		&p.ResultRetentionSeconds, &p.MetadataRetentionSeconds, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.Workload{}, false, 0, nil
	}
	if err != nil {
		return policy.Workload{}, false, 0, err
	}
	if maxOutputBytes != nil {
		p.MaxOutputBytes = *maxOutputBytes
		p.MaxOutputBytesExplicit = true
	}
	canonicalGeneration, err := policy.CanonicalJSON(p.AllowedGeneration)
	if err != nil {
		return policy.Workload{}, false, 0, fmt.Errorf("canonicalize durable allowed_generation: %w", err)
	}
	p.AllowedGeneration = canonicalGeneration
	// The local ID is only used internally for updates; policy.Workload stays
	// independent of PostgreSQL identity details.
	p.TopologyDigest = bytesToDigest(digest)
	var caps []string
	rows, err := q.Query(ctx, `SELECT capability FROM workload_policy_capabilities WHERE workload_id=$1 ORDER BY capability`, id)
	if err != nil {
		return policy.Workload{}, false, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var capability string
		if err := rows.Scan(&capability); err != nil {
			return policy.Workload{}, false, 0, err
		}
		caps = append(caps, capability)
	}
	p.AllowedCapabilities = caps
	return p, true, id, rows.Err()
}

func nullableOutputBytes(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func bytesToDigest(b []byte) [32]byte {
	var out [32]byte
	if len(b) == len(out) {
		copy(out[:], b)
	}
	return out
}

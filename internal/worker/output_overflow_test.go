package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
)

// oversizedAdapter returns a fixed successful response whose content exceeds
// the workload byte bound, proving the terminal path for output that passes
// inference but fails broker-side validation.
type oversizedAdapter struct {
	content string
}

func (a *oversizedAdapter) Type() string { return "oversized" }

func (a *oversizedAdapter) Infer(context.Context, *backend.Request) (*backend.Response, error) {
	return &backend.Response{
		Content:      a.content,
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 512, TotalTokens: 513},
	}, nil
}

// mustPolicyWithOutputBytes applies a workload with an explicit output-byte
// bound independent of the legacy tokens*4 derivation.
func mustPolicyWithOutputBytes(t *testing.T, name string, maxAttempts int, retryDelays []int, maxTokens, maxBytes int) postgres.WorkloadPolicy {
	t.Helper()
	digest := testTopologyDigest()
	p, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
		Name:                     uniqueName(name),
		PriorityClass:            "normal",
		MaxActiveTasks:           250,
		MaxAttempts:              maxAttempts,
		RetryDelaysSeconds:       retryDelays,
		MaxOutputTokens:          maxTokens,
		MaxOutputBytes:           maxBytes,
		MaxOutputBytesExplicit:   true,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		WorkloadTimeoutSeconds:   300,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		AllowedCapabilities:      []string{"instruct"},
		TopologyDigest:           digest[:],
	})
	if err != nil {
		t.Fatalf("apply byte-bound policy: %v", err)
	}
	testConfiguredWorkloads[p.Name] = digest
	return p
}

// TestByteOverflowFailsTerminalWithoutRetryOrResult drives an oversized
// provider response end to end: the task must fail terminally with
// output_invalid/output_bytes_exceeded, exactly one attempt (no retry), and
// no persisted result row. The 2049-byte content exceeds the explicit
// 2048-byte bound but would pass the legacy 2048*4 derivation, so the test
// also pins the explicit (non-derived) enforcement path.
func TestByteOverflowFailsTerminalWithoutRetryOrResult(t *testing.T) {
	clearTasks(t)
	client := mustClient(t)
	policy := mustPolicyWithOutputBytes(t, "wpolicy-bytes", 3, []int{1}, 2048, 2048)
	mustGrant(t, client.ID, policy.ID)
	payload := payloadFor(t, policy.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "hello"}}, textResponse)
	taskID := mustEnqueue(t, client.ID, policy.ID, uniqueName("wkey-0000000000000000"), payload)

	w, _, _ := newWorkerEnv(t, "none", 0)
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
		return &oversizedAdapter{content: strings.Repeat("x", 2049)}, nil
	})
	startWorker(t, w)

	task := waitForState(t, taskID, domain.TaskFailed, 10*time.Second)
	if task.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on output_bytes_exceeded)", task.Attempts)
	}
	att, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatalf("last attempt: %v", err)
	}
	if att.ErrorClass == nil || *att.ErrorClass != string(domain.ErrorOutputInvalid) {
		t.Errorf("error class = %v, want output_invalid", att.ErrorClass)
	}
	if att.ErrorCode == nil || *att.ErrorCode != "output_bytes_exceeded" {
		t.Errorf("error code = %v, want output_bytes_exceeded", att.ErrorCode)
	}
	// No result row may exist for a byte-overflow failure.
	if _, err := rtStore.GetResultByTaskID(context.Background(), taskID); !errors.Is(err, postgres.ErrNotFound) {
		t.Errorf("result row exists for byte-overflow task: %v", err)
	}
}

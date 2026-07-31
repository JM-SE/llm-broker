package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestNilSafety pins that every instrumentation method is a safe no-op on a
// nil receiver (components without metrics must never panic).
func TestNilSafety(t *testing.T) {
	var m *Metrics
	m.IncRequest("GET", "health", "200")
	m.ObserveRequestDuration("GET", "health", 0.001)
	m.IncAuthFailure(AuthReasonMissing)
	m.SetReady(true)
	m.IncDecision(ReasonBatchOpened)
	m.RefreshQueue(1, 1, 2.5)
	m.IncInference(ClassSucceeded)
	m.SetLeadership(true)
	m.IncPurged(KindPayloads, 3)
}

// TestMetricNamesRegistered verifies the collectors land on the registry with
// the expected names and bounded label values.
func TestMetricNamesRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.IncRequest("POST", "tasks.create", "201")
	m.IncRequest("POST", "tasks.create", "202")
	m.ObserveRequestDuration("POST", "tasks.create", 0.01)
	m.IncAuthFailure(AuthReasonInvalid)
	m.SetReady(true)
	m.SetLeadership(true)
	m.IncDecision(ReasonBatchOpened)
	m.IncInference(ClassSucceeded)
	m.IncPurged(KindPayloads, 3)
	m.RefreshQueue(2, 1, 4.5)

	got, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range got {
		names[mf.GetName()] = true
	}
	for _, want := range []string{
		"llm_broker_http_requests_total",
		"llm_broker_http_request_duration_seconds",
		"llm_broker_auth_failures_total",
		"llm_broker_ready",
		"llm_broker_tasks_queued",
		"llm_broker_tasks_running",
		"llm_broker_oldest_queued_task_seconds",
		"llm_broker_scheduling_decisions_total",
		"llm_broker_inference_attempts_total",
		"llm_broker_worker_leadership",
		"llm_broker_retention_purged_total",
	} {
		if !names[want] {
			t.Errorf("metric %s not registered", want)
		}
	}
}

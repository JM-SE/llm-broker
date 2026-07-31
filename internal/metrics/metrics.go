// Package metrics defines the bounded-label instrumentation surface of the
// broker. Every label set is fixed and operator-bounded: client identities,
// task identifiers, model names, URLs, and raw errors never appear as labels.
// A nil *Metrics is a valid no-op, so instrumentation is always safe to call.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metric label values (fixed, bounded sets).
const (
	ReasonBatchOpened    = "batch_opened"
	ReasonBatchContinued = "batch_continued"
	ReasonNoEligible     = "no_eligible"

	ClassSucceeded = "succeeded"
	ClassCancelled = "cancelled"

	AuthReasonMissing = "missing"
	AuthReasonInvalid = "invalid"

	KindPayloads = "payloads"
	KindResults  = "results"
	KindTasks    = "tasks"
)

// Metrics is the instrumented surface shared by the API, scheduler, worker,
// and retention components.
type Metrics struct {
	// API.
	RequestsTotal     *prometheus.CounterVec
	RequestDuration   *prometheus.HistogramVec
	AuthFailuresTotal *prometheus.CounterVec
	Ready             prometheus.Gauge
	// Queue and scheduling (refreshed by the worker).
	TasksQueued         prometheus.Gauge
	TasksRunning        prometheus.Gauge
	OldestQueuedSeconds prometheus.Gauge
	SchedulingDecisions *prometheus.CounterVec
	// Inference.
	InferenceAttempts *prometheus.CounterVec
	// Leadership.
	Leadership prometheus.Gauge
	// Retention.
	RetentionPurged *prometheus.CounterVec
}

// New builds all collectors registered on reg.
func New(reg *prometheus.Registry) *Metrics {
	f := promauto.With(reg)
	return &Metrics{
		RequestsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_broker_http_requests_total",
			Help: "HTTP requests by method, route pattern, and status code.",
		}, []string{"method", "path", "status"}),
		RequestDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "llm_broker_http_request_duration_seconds",
			Help:    "HTTP request duration by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path"}),
		AuthFailuresTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_broker_auth_failures_total",
			Help: "Failed authentication attempts by reason.",
		}, []string{"reason"}),
		Ready: f.NewGauge(prometheus.GaugeOpts{
			Name: "llm_broker_ready",
			Help: "1 when the service reports ready, 0 otherwise.",
		}),
		TasksQueued: f.NewGauge(prometheus.GaugeOpts{
			Name: "llm_broker_tasks_queued",
			Help: "Number of queued tasks.",
		}),
		TasksRunning: f.NewGauge(prometheus.GaugeOpts{
			Name: "llm_broker_tasks_running",
			Help: "Number of running tasks (product invariant: at most 1).",
		}),
		OldestQueuedSeconds: f.NewGauge(prometheus.GaugeOpts{
			Name: "llm_broker_oldest_queued_task_seconds",
			Help: "Age in seconds of the oldest queued task; 0 when the queue is empty.",
		}),
		SchedulingDecisions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_broker_scheduling_decisions_total",
			Help: "Scheduler claim outcomes by reason.",
		}, []string{"reason"}),
		InferenceAttempts: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_broker_inference_attempts_total",
			Help: "Inference attempts by outcome class (error classes are stable).",
		}, []string{"class"}),
		Leadership: f.NewGauge(prometheus.GaugeOpts{
			Name: "llm_broker_worker_leadership",
			Help: "1 when this process holds the execution lock, 0 otherwise.",
		}),
		RetentionPurged: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_broker_retention_purged_total",
			Help: "Rows purged by the retention job by kind.",
		}, []string{"kind"}),
	}
}

// --- HTTP ---------------------------------------------------------------

func (m *Metrics) IncRequest(method, path, status string) {
	if m != nil {
		m.RequestsTotal.WithLabelValues(method, path, status).Inc()
	}
}

func (m *Metrics) ObserveRequestDuration(method, path string, seconds float64) {
	if m != nil {
		m.RequestDuration.WithLabelValues(method, path).Observe(seconds)
	}
}

func (m *Metrics) IncAuthFailure(reason string) {
	if m != nil {
		m.AuthFailuresTotal.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) SetReady(v bool) {
	if m != nil {
		if v {
			m.Ready.Set(1)
		} else {
			m.Ready.Set(0)
		}
	}
}

// --- Scheduling -----------------------------------------------------------

func (m *Metrics) IncDecision(reason string) {
	if m != nil {
		m.SchedulingDecisions.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) RefreshQueue(queued, running int64, oldestSeconds float64) {
	if m == nil {
		return
	}
	m.TasksQueued.Set(float64(queued))
	m.TasksRunning.Set(float64(running))
	m.OldestQueuedSeconds.Set(oldestSeconds)
}

// --- Inference ------------------------------------------------------------

func (m *Metrics) IncInference(class string) {
	if m != nil {
		m.InferenceAttempts.WithLabelValues(class).Inc()
	}
}

// --- Leadership -----------------------------------------------------------

func (m *Metrics) SetLeadership(v bool) {
	if m != nil {
		if v {
			m.Leadership.Set(1)
		} else {
			m.Leadership.Set(0)
		}
	}
}

// --- Retention ------------------------------------------------------------

func (m *Metrics) IncPurged(kind string, n int64) {
	if m != nil && n > 0 {
		m.RetentionPurged.WithLabelValues(kind).Add(float64(n))
	}
}

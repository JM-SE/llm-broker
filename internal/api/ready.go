package api

import (
	"net/http"
	"time"
)

// handleHealth is process liveness only: no dependency checks.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady reports whether the service can process tasks reliably:
// database reachable and migrated, exactly one active worker holding the
// execution lock, and no unresolved ambiguous execution (which blocks all
// inference). 503 otherwise.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	fail := func(detail string) {
		s.setReady(false)
		writeProblem(w, http.StatusServiceUnavailable, codeUnavailable,
			"not ready", detail)
	}
	if err := s.store.Ping(r.Context()); err != nil {
		fail("database unreachable")
		return
	}
	if err := s.store.CheckSchemaCompatible(r.Context()); err != nil {
		fail("database schema version mismatch")
		return
	}
	workers, err := s.store.ListActiveWorkers(r.Context(), 30*time.Second)
	if err != nil {
		s.log.Error("readiness worker check failed", "request_id", requestIDFrom(r.Context()), "error", err)
		fail("worker state unavailable")
		return
	}
	if len(workers) == 0 {
		fail("no active worker holds the execution lock")
		return
	}
	blocked, err := s.store.HasUnresolvedIndeterminate(r.Context())
	if err != nil {
		s.log.Error("readiness recovery check failed", "request_id", requestIDFrom(r.Context()), "error", err)
		fail("worker state unavailable")
		return
	}
	if blocked {
		fail("an ambiguous execution requires operator action")
		return
	}
	s.setReady(true)
	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
}

func (s *Server) setReady(v bool) {
	s.metrics.SetReady(v)
}

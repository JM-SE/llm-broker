package api

import "net/http"

// handleHealth is process liveness only: no dependency checks.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady reports whether the service can process tasks reliably.
// At this phase readiness covers the database: reachable, migrated, and
// schema-compatible. Worker/backend state joins in later phases.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, codeUnavailable,
			"not ready", "database unreachable")
		return
	}
	if err := s.store.CheckSchemaCompatible(r.Context()); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, codeUnavailable,
			"not ready", "database schema version mismatch")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
}

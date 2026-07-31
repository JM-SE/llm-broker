package api

import (
	"net/http"
	"strings"
)

// routeLabel maps a request to a stable route name for logs and metrics.
// Raw paths contain task identifiers and must never reach logs or metric
// labels; only these fixed names are used.
func routeLabel(r *http.Request) string {
	method := r.Method
	path := r.URL.Path
	switch {
	case method == http.MethodGet && path == "/health":
		return "health"
	case method == http.MethodGet && path == "/ready":
		return "ready"
	case method == http.MethodGet && path == "/metrics":
		return "metrics"
	case method == http.MethodPost && path == "/v1/tasks":
		return "tasks.create"
	case strings.HasPrefix(path, "/v1/tasks/") && strings.HasSuffix(path, "/result"):
		return "tasks.result"
	case strings.HasPrefix(path, "/v1/tasks/") && strings.HasSuffix(path, "/cancel"):
		return "tasks.cancel"
	case strings.HasPrefix(path, "/v1/tasks/"):
		return "tasks.get"
	}
	return "other"
}

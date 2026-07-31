package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JM-SE/llm-broker/internal/auth"
)

// context keys for per-request state.
type ctxKey int

const (
	ctxKeyClient ctxKey = iota
	ctxKeyRequestID
)

// clientFrom returns the authenticated identity attached by requireAuth.
func clientFrom(ctx context.Context) (auth.Identity, bool) {
	ident, ok := ctx.Value(ctxKeyClient).(auth.Identity)
	return ident, ok
}

// requestIDFrom returns the correlation id attached by requestID.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// requestID assigns an opaque correlation id and echoes it as X-Request-Id.
// The id is the only linkage between a request and its log lines.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			copy(b[:], "000000000000")
		}
		id := hex.EncodeToString(b[:])
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanic converts handler panics into sanitized 500 responses.
func recoverPanic(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic recovered",
					"request_id", requestIDFrom(r.Context()),
					"path", r.URL.Path,
					"panic", fmt.Sprint(rec))
				writeProblem(w, http.StatusInternalServerError, codeInternal,
					"internal error", "the request could not be completed")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusWriter captures the response status for sanitized logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// logRequests records sanitized request metadata only: method, path, status,
// duration, and correlation id. Request bodies, credentials, headers, and
// query values are never logged.
func logRequests(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Info("request",
			"request_id", requestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// requireAuth authenticates the bearer credential and attaches the client
// identity. Malformed and invalid credentials get the same response.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			writeProblem(w, http.StatusUnauthorized, codeUnauthorized,
				"authentication required", "a bearer credential is required")
			return
		}
		ident, err := s.verifier.Verify(r.Context(), fields[1])
		if err != nil {
			if errors.Is(err, auth.ErrMalformedToken) || errors.Is(err, auth.ErrInvalidToken) {
				writeProblem(w, http.StatusUnauthorized, codeUnauthorized,
					"authentication failed", "invalid credential")
				return
			}
			s.log.Error("credential lookup failed",
				"request_id", requestIDFrom(r.Context()), "error", err)
			writeProblem(w, http.StatusInternalServerError, codeInternal,
				"internal error", "the request could not be authenticated")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyClient, ident)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

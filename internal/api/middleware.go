package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// middleware wraps the router with auth, CORS, and access logging in the
// order: CORS → auth → log → handler. CORS runs first so preflight requests
// short-circuit before auth.
func (s *Server) middleware(next http.Handler) http.Handler {
	return s.corsMiddleware(s.authMiddleware(s.logMiddleware(next)))
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.cfg.CORSOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health is unauthenticated so liveness probes don't need the token.
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.TrimSpace(s.cfg.Token) == "" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			s.log401(r, "missing or non-bearer Authorization header")
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		// ConstantTimeCompare avoids a timing side-channel that would let an
		// attacker recover the token byte-by-byte by measuring response latency
		// across many requests. The byte-length check is constant-time-safe
		// because Tokens are configured at startup and never user-controlled
		// in length, so leaking that the lengths differ does not leak content.
		got := []byte(strings.TrimSpace(auth[len(prefix):]))
		want := []byte(s.cfg.Token)
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			s.log401(r, "bearer token mismatch")
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// log401 records a rejected authentication attempt. authMiddleware is the
// OUTERMOST handler-wrapper (CORS → auth → log → handler), so a 401 short-
// circuits before logMiddleware runs and the rejection would otherwise be
// silent in stderr — which makes prod credential rotations or attacks
// invisible. We log unconditionally rather than gated on s.cfg.Debug so
// "why is my dashboard returning 401?" is answerable from the logs.
func (s *Server) log401(r *http.Request, reason string) {
	s.logger.Printf("[api] 401 %s %s from %s — %s",
		r.Method, r.URL.RequestURI(), r.RemoteAddr, reason)
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if s.cfg.Debug || rec.status >= 400 {
			s.logger.Printf("[api] %s %s -> %d (%s)", r.Method, r.URL.RequestURI(), rec.status, time.Since(start))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

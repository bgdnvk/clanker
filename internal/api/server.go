// Package api hosts the HTTP API layer that wraps the clanker agent.
//
// Phase 4 (per the planning doc) — exposes Tencent Cloud inventory and
// helper endpoints as JSON so the React/Vite dashboard (Phase 5+) can
// drive the agent without shelling out to the CLI.
//
// The server uses stdlib net/http (Go 1.22+ pattern routing) — no third
// party router needed at this scale.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Config controls how the HTTP server is bound and how requests are
// authorised. Empty Token disables auth (a loud warning is logged).
type Config struct {
	Addr        string // listen address, e.g. ":8080"
	Token       string // bearer token; if empty, auth is disabled
	CORSOrigin  string // value for Access-Control-Allow-Origin; "*" by default
	ReadTimeout time.Duration
	WriteTimeout time.Duration
	Debug       bool
}

// Server wraps an *http.Server plus the routes the API exposes. Build it
// with New and start with Run; Run blocks until ctx is cancelled.
type Server struct {
	cfg    Config
	mux    *http.ServeMux
	logger *log.Logger
	started time.Time
	history *history
}

// New constructs a Server with the standard route set. Call Run to start.
func New(cfg Config, logger *log.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.CORSOrigin == "" {
		cfg.CORSOrigin = "*"
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 90 * time.Second
	}
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), logger: logger, started: time.Now(), history: newHistory()}
	s.registerRoutes()
	return s
}

// Run starts the HTTP server and blocks until ctx is cancelled or
// ListenAndServe returns an error.
func (s *Server) Run(ctx context.Context) error {
	if strings.TrimSpace(s.cfg.Token) == "" {
		s.logger.Println("[api] WARNING: no token set — server is open. Pass --token or set CLANKER_API_TOKEN.")
	}
	srv := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      s.middleware(s.mux),
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Printf("[api] listening on %s", s.cfg.Addr)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// writeJSON encodes v as a JSON response with the given status code and the
// canonical `Content-Type: application/json` header. Used by every handler so
// the response shape stays uniform.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] writeJSON: %v", err)
	}
}

// writeError responds with a uniform error envelope so frontend code can
// branch on `error.code` instead of parsing free-form messages.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// writeData wraps a successful response in `{ "data": ... }` envelope.
func writeData(w http.ResponseWriter, v interface{}) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": v})
}

// writeRaw is used for endpoints whose source is already JSON-encoded
// (e.g. Tencent context gather funcs), so we don't double-encode.
func writeRawData(w http.ResponseWriter, rawJSON string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"data":%s}`, rawJSON)
}

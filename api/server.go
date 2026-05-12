package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/store"
)

// Server hosts the OpenAI-compatible HTTP API. It binds to 127.0.0.1 by
// default; Tailscale Serve handles TLS termination + routing externally.
type Server struct {
	agent          *agent.Agent
	db             *store.Store
	cfg            config.APIConfig
	requestTimeout time.Duration
	logger         *slog.Logger
	srv            *http.Server
}

// NewServer wires routes and prepares the listening server. ListenAndServe
// is called from Run.
func NewServer(ag *agent.Agent, db *store.Store, cfg config.APIConfig, logger *slog.Logger) *Server {
	s := &Server{
		agent:          ag,
		db:             db,
		cfg:            cfg,
		requestTimeout: 5 * time.Minute,
		logger:         logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.cors(s.handleRoot))
	mux.HandleFunc("/healthz", s.cors(s.handleHealth))
	mux.HandleFunc("/v1/models", s.cors(s.authMiddleware(s.handleModels)))
	mux.HandleFunc("/v1/chat/completions", s.cors(s.authMiddleware(s.handleChatCompletions)))

	s.srv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No top-level write timeout: streaming responses can run minutes.
		// Per-request timeout is enforced via context inside the handler.
	}
	return s
}

// Run blocks serving HTTP until ctx is cancelled, at which point it does
// graceful shutdown with a 5-second drain window.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("api server listening", "addr", s.cfg.Listen)
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("api server shutdown", "error", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// handleHealth is a tiny unauthenticated probe so reverse-proxies can
// readiness-check without a bearer.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// cors is a permissive CORS middleware. Browser-based clients (OpenWebUI in
// a tab) issue a CORS preflight OPTIONS before the actual POST. Without
// these headers the browser refuses with "Network Error: Load failed",
// which surfaces in the UI as a generic error with no useful detail.
//
// We set Allow-Origin to * because (a) deployments live behind Tailscale
// and (b) auth is bearer-based, not cookie-based — credentials aren't
// implicitly carried, so the standard "* + Authorization" combo is safe.
// If torii ever speaks public-internet TLS without Tailscale gating,
// tighten this to a config-driven origin list.
func (s *Server) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// handleRoot answers the catch-all path. The default ServeMux routes any
// unmatched request here, so we use it both as a friendly landing page and
// to surface 404s for unexpected paths (e.g. /v1/embeddings) in a useful
// JSON shape rather than the stdlib's plaintext "404 page not found".
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"torii","object":"info","endpoints":["/v1/models","/v1/chat/completions","/healthz"]}`))
		return
	}
	s.logger.Info("api: 404", "path", r.URL.Path, "method", r.Method)
	writeJSONError(w, http.StatusNotFound, fmt.Sprintf("path %q not found", r.URL.Path))
}

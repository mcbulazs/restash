// Package server is restash's HTTP side: the JSON API for the web UI and
// agents, the agents' websocket link, the restic repo proxy and the web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/store"
	"github.com/mcbulazs/restash/internal/trash"
)

// Config sets up a Server.
type Config struct {
	PublicURL *url.URL     // e.g. https://restash.bulazs.com
	Store     *store.Store //
	Repos     *trash.Proxy // the delete-to-trash proxy in front of rest-server
	ReposDir  string       // where the repos are on disk, for sizes
	Storage   string       // the storage folder, for free space
	Web       fs.FS        // the built web UI; nil to serve none
	Version   string       //
	Now       func() time.Time
}

// Server handles all HTTP requests.
type Server struct {
	cfg      Config
	store    *store.Store
	hub      *hub
	sizes    *sizeCache
	limits   *limiter
	fakeSalt []byte
	now      func() time.Time
}

// New builds a Server.
func New(ctx context.Context, cfg Config) (*Server, error) {
	secret, err := cfg.Store.Secret(ctx, "fake-salt")
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		cfg:      cfg,
		store:    cfg.Store,
		hub:      newHub(),
		sizes:    newSizeCache(cfg.ReposDir, 5*time.Minute),
		limits:   newLimiter(),
		fakeSalt: secret,
		now:      now,
	}, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	// Accounts
	mux.HandleFunc("GET /api/auth/params", s.handleParams)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/invite/{token}", s.handleInviteInfo)
	mux.HandleFunc("POST /api/auth/signup", s.handleSignup)
	mux.HandleFunc("POST /api/auth/recover/key", s.handleRecoverKey)
	mux.HandleFunc("POST /api/auth/recover", s.handleRecover)
	mux.Handle("GET /api/me", s.user(s.handleMe))

	// PCs
	mux.Handle("GET /api/pcs", s.user(s.handlePCs))
	mux.Handle("POST /api/pcs/join", s.user(s.handleCreateJoinCode))
	mux.Handle("GET /api/pcs/join/{code}", s.user(s.handleJoinStatus))
	mux.Handle("GET /api/pcs/{pc}", s.user(s.handlePC))
	mux.Handle("POST /api/pcs/{pc}/entries", s.user(s.handleCreateEntry))
	mux.Handle("PUT /api/pcs/{pc}/entries/{entry}", s.user(s.handleUpdateEntry))
	mux.Handle("DELETE /api/pcs/{pc}/entries/{entry}", s.user(s.handleDeleteEntry))
	mux.Handle("POST /api/pcs/{pc}/run", s.user(s.handleRunNow))
	mux.Handle("POST /api/pcs/{pc}/browse", s.user(s.handleBrowse))

	// Admin
	mux.Handle("GET /api/people", s.admin(s.handlePeople))
	mux.Handle("POST /api/invites", s.admin(s.handleCreateInvite))
	mux.Handle("DELETE /api/invites/{token}", s.admin(s.handleDeleteInvite))

	// Agents
	mux.HandleFunc("POST /api/agent/join", s.handleAgentJoin)
	mux.Handle("GET /api/agent/account", s.agent(s.handleAgentAccount))
	mux.Handle("PUT /api/agent/key", s.agent(s.handleAgentSetKey))
	mux.Handle("POST /api/agent/backups", s.agent(s.handleAgentBackup))
	mux.Handle("GET /api/agent/entries", s.agent(s.handleAgentEntries))
	mux.Handle("GET /api/agent/ws", s.agent(s.handleAgentWS))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { httpError(w, http.StatusNotFound, "not found") })

	// restic repos
	mux.Handle("/repo/", http.StripPrefix("/repo", s.repoAuth(s.cfg.Repos)))

	if s.cfg.Web != nil {
		mux.Handle("/", webHandler(s.cfg.Web))
	}

	// Browsers send Sec-Fetch-Site/Origin; cross-site writes are refused.
	// Agents send neither and are let through (they use a bearer token).
	cop := http.NewCrossOriginProtection()
	return securityHeaders(cop.Handler(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/repo/") {
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			h.Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

// apiError is the JSON error body.
type apiError struct {
	Error string `json:"error"`
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	httpError(w, http.StatusInternalServerError, "something went wrong on the server")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// readJSON decodes a small JSON body into v.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

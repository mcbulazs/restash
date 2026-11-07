package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/store"
)

// Storage limits: the admin can cap how much a person's PCs store together.
// The agent checks before each backup and doesn't start one over the
// limit. The repo refuses new data only well past it (limitSlack), so a
// backup that crosses the line can finish and cleaning up, which rewrites
// some data, still works.

// limitSlack is how far past the limit the repo still takes new data.
const limitSlack = 10 // percent

// maxLimit keeps nonsense out (1 PB).
const maxLimit = 1 << 50

// usage is how much a person's PCs take on the server. The trash isn't
// counted: it empties itself.
func (s *Server) usage(ctx context.Context, userID string) (int64, error) {
	pcs, err := s.store.PCs(ctx, userID)
	if err != nil {
		return 0, err
	}
	var used int64
	for _, p := range pcs {
		used += s.sizes.size(p.ID)
	}
	return used, nil
}

func (s *Server) storageOf(ctx context.Context, u *store.User) (proto.Storage, error) {
	used, err := s.usage(ctx, u.ID)
	return proto.Storage{Used: used, Limit: u.StorageLimit}, err
}

// handleStorage is a person's own usage and limit.
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	st, err := s.storageOf(r.Context(), userFrom(r.Context()))
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleAgentStorage is the PC owner's usage and limit, checked before
// each backup.
func (s *Server) handleAgentStorage(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByID(r.Context(), pcFrom(r.Context()).UserID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	st, err := s.storageOf(r.Context(), u)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSetLimit lets the admin set someone's limit (0: none).
func (s *Server) handleSetLimit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Limit int64 `json:"limit"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Limit < 0 || req.Limit > maxLimit {
		httpError(w, http.StatusBadRequest, "that's not a size restash can use")
		return
	}
	switch err := s.store.SetStorageLimit(r.Context(), r.PathValue("id"), req.Limit); {
	case isNotFound(err):
		httpError(w, http.StatusNotFound, "no such person")
	case err != nil:
		internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// overLimit says whether a write of new backup data to a repo should be
// refused: its owner is past their limit plus the slack.
func (s *Server) overLimit(ctx context.Context, userID string) bool {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil || u.StorageLimit <= 0 {
		return false
	}
	used, err := s.usage(ctx, userID)
	return err == nil && used > u.StorageLimit+u.StorageLimit/100*limitSlack
}

// growsRepo says whether a request adds backup data (as opposed to locks,
// reads and deletes, which stay allowed so space can be freed).
func growsRepo(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) >= 3 && (parts[1] == "data" || parts[1] == "snapshots")
}

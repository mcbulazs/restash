package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/store"
)

type personJSON struct {
	userJSON
	CreatedAt time.Time `json:"createdAt"`
	PCs       int       `json:"pcs"`
	Stored    int64     `json:"stored"`
	// Oldest last good backup among their PCs; nil if a PC never backed up.
	OldestOK *time.Time `json:"oldestOk"`
	AllSafe  bool       `json:"allSafe"`
	Limit    int64      `json:"limit"`   // storage limit in bytes; 0: none
	Problem  string     `json:"problem"` // worst problem among their PCs
}

// worse picks the more serious of two problem kinds.
func worse(a, b string) string {
	rank := map[string]int{"": 0, "late": 1, "quiet": 2, "failed": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

type inviteJSON struct {
	Token     string    `json:"token"`
	Email     string    `json:"email"`
	Link      string    `json:"link"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type peopleJSON struct {
	People  []personJSON `json:"people"`
	Invites []inviteJSON `json:"invites"`
	Space   spaceJSON    `json:"space"`
}

type spaceJSON struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

func (s *Server) inviteLink(token string) string {
	return strings.TrimSuffix(s.cfg.PublicURL.String(), "/") + "/signup?invite=" + token
}

// handlePeople is the admin's overview. It shows names, PC counts, sizes and
// backup times; never anything about files.
func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.store.Users(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	pcs, err := s.store.PCs(ctx, "")
	if err != nil {
		internalError(w, r, err)
		return
	}
	_, lastOK, err := s.store.LastBackups(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	states, _, err := s.statuses(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	byUser := map[string][]store.PC{}
	for _, p := range pcs {
		byUser[p.UserID] = append(byUser[p.UserID], p)
	}
	out := peopleJSON{People: []personJSON{}, Invites: []inviteJSON{}}
	for _, u := range users {
		p := personJSON{userJSON: toUserJSON(&u), CreatedAt: u.CreatedAt, PCs: len(byUser[u.ID]), AllSafe: len(byUser[u.ID]) > 0, Limit: u.StorageLimit}
		never := false
		for _, pc := range byUser[u.ID] {
			p.Stored += s.sizes.size(pc.ID)
			t, ok := lastOK[pc.ID]
			if !ok {
				never = true
				p.AllSafe = false
				continue
			}
			if p.OldestOK == nil || t.Before(*p.OldestOK) {
				tt := t
				p.OldestOK = &tt
			}
			if states[pc.ID].Problem != "" {
				p.AllSafe = false
				p.Problem = worse(p.Problem, states[pc.ID].Problem)
			}
		}
		if never {
			p.OldestOK = nil
		}
		out.People = append(out.People, p)
	}
	invites, err := s.store.Invites(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	for _, inv := range invites {
		if inv.Admin && inv.Email == "" {
			continue // the first-run setup link
		}
		out.Invites = append(out.Invites, inviteJSON{Token: inv.Token, Email: inv.Email, Link: s.inviteLink(inv.Token), CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt})
	}
	out.Space.Total, out.Space.Used = diskUsage(s.cfg.Storage)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	email := store.NormalizeEmail(req.Email)
	if !validEmail(email) {
		httpError(w, http.StatusBadRequest, "that doesn't look like an email address")
		return
	}
	if _, err := s.store.UserByEmail(r.Context(), email); err == nil {
		httpError(w, http.StatusConflict, store.ErrEmailTaken.Error())
		return
	}
	inv, err := s.store.CreateInvite(r.Context(), email, false, userFrom(r.Context()).ID, store.InviteLength)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteJSON{Token: inv.Token, Email: inv.Email, Link: s.inviteLink(inv.Token), CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt})
}

func (s *Server) handleDeleteInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteInvite(r.Context(), r.PathValue("token")); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

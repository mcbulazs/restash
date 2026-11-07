package server

import (
	"net/http"
	"time"

	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/status"
	"github.com/mcbulazs/restash/internal/store"
)

type runJSON struct {
	At time.Time `json:"at"`
	OK bool      `json:"ok"`
}

type pcJSON struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	OS           string              `json:"os"`
	Arch         string              `json:"arch"`
	AgentVersion string              `json:"agentVersion"`
	Ready        bool                `json:"ready"` // has its key and repo
	Online       bool                `json:"online"`
	CreatedAt    time.Time           `json:"createdAt"`
	LastSeen     *time.Time          `json:"lastSeen"`
	LastBackup   *time.Time          `json:"lastBackup"`
	LastOK       *time.Time          `json:"lastOk"`
	Stored       int64               `json:"stored"`
	Recent       []runJSON           `json:"recent"` // last 30 days, oldest first
	Schedules    []schedule.Schedule `json:"schedules"`
	Problem      string              `json:"problem"` // failed, quiet, late or ""
	Problems     []status.Problem    `json:"problems"`
	Running      []progressJSON      `json:"running"` // backups running now
}

func ptr(t time.Time, ok bool) *time.Time {
	if !ok {
		return nil
	}
	return &t
}

// describePCs adds status and history to PCs.
func (s *Server) describePCs(r *http.Request, pcs []store.PC) ([]pcJSON, error) {
	last, lastOK, err := s.store.LastBackups(r.Context())
	if err != nil {
		return nil, err
	}
	recent, err := s.store.BackupsSince(r.Context(), s.now().Add(-31*24*time.Hour))
	if err != nil {
		return nil, err
	}
	entries, err := s.store.AllEntries(r.Context())
	if err != nil {
		return nil, err
	}
	states, _, err := s.statuses(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]pcJSON, 0, len(pcs))
	for _, p := range pcs {
		l, hasL := last[p.ID]
		lo, hasLO := lastOK[p.ID]
		j := pcJSON{
			ID: p.ID, Name: p.Name, OS: p.OS, Arch: p.Arch, AgentVersion: p.AgentVersion,
			Ready: p.PCKey != "", Online: s.hub.online(p.ID),
			CreatedAt: p.CreatedAt, LastSeen: p.LastSeen,
			LastBackup: ptr(l, hasL), LastOK: ptr(lo, hasLO),
			Stored: s.sizes.size(p.ID),
			Recent: []runJSON{}, Schedules: []schedule.Schedule{},
			Problem: states[p.ID].Problem, Problems: states[p.ID].Problems,
			Running: s.progress.list(p.ID, s.now()),
		}
		if j.Problems == nil {
			j.Problems = []status.Problem{}
		}
		for _, e := range entries[p.ID] {
			j.Schedules = append(j.Schedules, e.Schedule)
		}
		if j.Online {
			now := s.now()
			j.LastSeen = &now
		}
		for _, b := range recent[p.ID] {
			j.Recent = append(j.Recent, runJSON{At: b.FinishedAt, OK: b.OK})
		}
		out = append(out, j)
	}
	return out, nil
}

func (s *Server) handlePCs(w http.ResponseWriter, r *http.Request) {
	pcs, err := s.store.PCs(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	out, err := s.describePCs(r, pcs)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type joinCodeJSON struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
	Server    string    `json:"server"`
}

func (s *Server) handleCreateJoinCode(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if !s.limits.allow("joincode:"+u.ID, 20, time.Hour) {
		httpError(w, http.StatusTooManyRequests, "too many codes; wait a while")
		return
	}
	code, exp, err := s.store.CreateJoinCode(r.Context(), u.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, joinCodeJSON{Code: code, ExpiresAt: exp, Server: s.cfg.PublicURL.String()})
}

// handleJoinStatus is polled by the "Add a PC" page.
func (s *Server) handleJoinStatus(w http.ResponseWriter, r *http.Request) {
	pc, err := s.store.JoinStatus(r.Context(), userFrom(r.Context()).ID, r.PathValue("code"))
	switch {
	case isNotFound(err):
		writeJSON(w, http.StatusOK, map[string]string{"status": "expired"})
	case err != nil:
		internalError(w, r, err)
	case pc == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "waiting"})
	default:
		status := "joined"
		if pc.PCKey != "" {
			status = "ready"
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "pc": map[string]string{"id": pc.ID, "name": pc.Name, "os": pc.OS}})
	}
}

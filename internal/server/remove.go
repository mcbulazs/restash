package server

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"

	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
)

// Removing an entry's backups, a PC or a person. The PC owner can do the
// first two for their own PCs; the admin can do all three for anyone.
//
// Snapshots can only be removed by the PC (only it has the key), so that
// part is a request the PC carries out when it's next online: it forgets
// them, prunes, and says it's done. Removed data goes through the trash.

// backupEntryPattern matches what backups are filed under: an entry id, or
// "manual" for backups made by hand.
var backupEntryPattern = regexp.MustCompile(`^([a-z2-7]{16}|manual)$`)

// pcFor finds the PC in the path for its owner, or for the admin too.
func (s *Server) pcFor(w http.ResponseWriter, r *http.Request, adminToo bool) *store.PC {
	u := userFrom(r.Context())
	pc, err := s.store.PCByID(r.Context(), r.PathValue("pc"))
	if err == nil && pc.UserID != u.ID && !(adminToo && u.Admin) {
		err = store.ErrNotFound
	}
	if isNotFound(err) {
		httpError(w, http.StatusNotFound, "no such PC")
		return nil
	}
	if err != nil {
		internalError(w, r, err)
		return nil
	}
	return pc
}

// purge asks the PC to remove an entry's backups and tells it now if it's
// online.
func (s *Server) purge(r *http.Request, pcID, entry string) error {
	if err := s.store.AddPurge(r.Context(), pcID, entry); err != nil {
		return err
	}
	s.entriesChanged(r, pcID) // the agent fetches purges with its entries
	return nil
}

// handlePurge removes the backups of an entry that no longer exists.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	pc := s.pcFor(w, r, true)
	if pc == nil {
		return
	}
	var req struct {
		Entry string `json:"entry"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !backupEntryPattern.MatchString(req.Entry) {
		httpError(w, http.StatusBadRequest, "bad entry")
		return
	}
	if _, err := s.store.Entry(r.Context(), pc.ID, req.Entry); err == nil {
		httpError(w, http.StatusConflict, "that entry still exists: delete the entry with its backups instead")
		return
	}
	if err := s.purge(r, pc.ID, req.Entry); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removePC unlinks a PC, forgets it and moves its repo to the trash.
func (s *Server) removePC(ctx context.Context, pc *store.PC) error {
	if err := s.store.DeletePC(ctx, pc.ID); err != nil {
		return err
	}
	s.hub.kick(pc.ID)
	s.progress.clear(pc.ID)
	s.sizes.forget(pc.ID)
	return s.cfg.Repos.TrashRepo(pc.ID)
}

func (s *Server) handleRemovePC(w http.ResponseWriter, r *http.Request) {
	pc := s.pcFor(w, r, true)
	if pc == nil {
		return
	}
	if err := s.removePC(r.Context(), pc); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRemovePerson removes an account with all its PCs (admin only).
func (s *Server) handleRemovePerson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == userFrom(ctx).ID {
		httpError(w, http.StatusConflict, "you can't remove your own account")
		return
	}
	if _, err := s.store.UserByID(ctx, id); isNotFound(err) {
		httpError(w, http.StatusNotFound, "no such person")
		return
	}
	pcs, err := s.store.PCs(ctx, id)
	if err != nil {
		internalError(w, r, err)
		return
	}
	for i := range pcs {
		if err := s.removePC(ctx, &pcs[i]); err != nil {
			internalError(w, r, err)
			return
		}
	}
	if err := s.store.DeleteUser(ctx, id); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The admin's view of one person: their PCs and entries, without names
// (those are locked with the person's key), so entries can be cleaned up.

type adminEntryJSON struct {
	ID        string            `json:"id"`
	Schedule  schedule.Schedule `json:"schedule"`
	CreatedAt time.Time         `json:"createdAt"`
	LastOK    *runDetailJSON    `json:"lastOk"`
}

type adminPCJSON struct {
	pcJSON
	Entries   []adminEntryJSON `json:"entries"`
	Leftovers []store.Leftover `json:"leftovers"`
	Purges    []string         `json:"purges"`
}

func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := s.store.UserByID(ctx, r.PathValue("id"))
	if isNotFound(err) {
		httpError(w, http.StatusNotFound, "no such person")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	pcs, err := s.store.PCs(ctx, u.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	desc, err := s.describePCs(r, pcs)
	if err != nil {
		internalError(w, r, err)
		return
	}
	st, err := s.storageOf(ctx, u)
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := struct {
		Person  userJSON      `json:"person"`
		Storage any           `json:"storage"`
		PCs     []adminPCJSON `json:"pcs"`
	}{toUserJSON(u), st, []adminPCJSON{}}
	for _, d := range desc {
		p := adminPCJSON{pcJSON: d, Entries: []adminEntryJSON{}}
		if p.Leftovers, err = s.store.Leftovers(ctx, d.ID); err != nil {
			internalError(w, r, err)
			return
		}
		if p.Purges, err = s.store.Purges(ctx, d.ID); err != nil {
			internalError(w, r, err)
			return
		}
		entries, err := s.store.Entries(ctx, d.ID)
		if err != nil {
			internalError(w, r, err)
			return
		}
		last, err := s.store.LastEntryBackups(ctx, d.ID)
		if err != nil {
			internalError(w, r, err)
			return
		}
		for _, e := range entries {
			j := adminEntryJSON{ID: e.ID, Schedule: e.Schedule, CreatedAt: e.CreatedAt}
			if b, ok := last[e.ID]; ok {
				run := toRun(b)
				run.Detail = "" // locked anyway, and not the admin's
				j.LastOK = &run
			}
			p.Entries = append(p.Entries, j)
		}
		out.PCs = append(out.PCs, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// The agent's side: what to remove, and saying it's done.

func (s *Server) handleAgentPurges(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Purges(r.Context(), pcFrom(r.Context()).ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAgentPurgeDone(w http.ResponseWriter, r *http.Request) {
	pc := pcFrom(r.Context())
	switch err := s.store.PurgeDone(r.Context(), pc.ID, r.PathValue("entry")); {
	case isNotFound(err):
		httpError(w, http.StatusNotFound, "no such request")
	case err != nil:
		internalError(w, r, err)
	default:
		s.sizes.forget(pc.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// kick closes a PC's link (it was removed); its token no longer works, so
// it won't come back.
func (h *hub) kick(id string) {
	h.mu.Lock()
	c := h.conns[id]
	delete(h.conns, id)
	h.mu.Unlock()
	if c != nil {
		c.c.Close(websocket.StatusPolicyViolation, "this PC was removed")
	}
}

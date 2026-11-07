package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
)

// validSealed checks a value looks like vault.Seal output.
func validSealed(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) >= 12+16
}

// ownPC loads the PC in the URL if it belongs to the logged-in user.
func (s *Server) ownPC(w http.ResponseWriter, r *http.Request) *store.PC {
	pc, err := s.store.PCByID(r.Context(), r.PathValue("pc"))
	if err == nil && pc.UserID != userFrom(r.Context()).ID {
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

type entryJSON struct {
	ID        string            `json:"id"`
	Schedule  schedule.Schedule `json:"schedule"`
	Config    string            `json:"config"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	LastOK    *runDetailJSON    `json:"lastOk"`
}

type runDetailJSON struct {
	Entry     string    `json:"entry"`
	StartedAt time.Time `json:"startedAt"`
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Files     int64     `json:"files"`
	Bytes     int64     `json:"bytes"`
	Added     int64     `json:"added"`
	Detail    string    `json:"detail,omitempty"`
}

func toRun(b store.Backup) runDetailJSON {
	return runDetailJSON{Entry: b.Entry, StartedAt: b.StartedAt, At: b.FinishedAt, OK: b.OK, Files: b.Files, Bytes: b.Bytes, Added: b.Added, Detail: b.Detail}
}

// handlePC is everything the PC page needs. Entry settings and run details
// stay locked; the browser opens them with the PC's key.
func (s *Server) handlePC(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	ctx := r.Context()
	desc, err := s.describePCs(r, []store.PC{*pc})
	if err != nil {
		internalError(w, r, err)
		return
	}
	entries, err := s.store.Entries(ctx, pc.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	last, err := s.store.LastEntryBackups(ctx, pc.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	runs, err := s.store.PCBackups(ctx, pc.ID, s.now().Add(-31*24*time.Hour))
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := struct {
		pcJSON
		PCKey    string          `json:"pcKey"`
		Timezone string          `json:"timezone"`
		Entries  []entryJSON     `json:"entries"`
		Runs     []runDetailJSON `json:"runs"`
	}{pcJSON: desc[0], PCKey: pc.PCKey, Timezone: pc.Timezone, Entries: []entryJSON{}, Runs: []runDetailJSON{}}
	for _, e := range entries {
		j := entryJSON{ID: e.ID, Schedule: e.Schedule, Config: e.Config, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
		if b, ok := last[e.ID]; ok {
			run := toRun(b)
			j.LastOK = &run
		}
		out.Entries = append(out.Entries, j)
	}
	for _, b := range runs {
		out.Runs = append(out.Runs, toRun(b))
	}
	writeJSON(w, http.StatusOK, out)
}

type entryRequest struct {
	ID       string            `json:"id"`
	Schedule schedule.Schedule `json:"schedule"`
	Config   string            `json:"config"`
}

// Entry ids are made by the browser, because the locked config is bound to
// the id.
var entryIDPattern = regexp.MustCompile(`^[a-z2-7]{16}$`)

func (req entryRequest) check() error {
	if err := req.Schedule.Validate(); err != nil {
		return err
	}
	if len(req.Config) > 64*1024 || !validSealed(req.Config) {
		return errors.New("bad entry settings")
	}
	return nil
}

func (s *Server) entriesChanged(r *http.Request, pcID string) {
	s.hub.send(r.Context(), pcID, proto.Message{Type: proto.TypeEntries}) // offline PCs fetch on connect
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	var req entryRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !entryIDPattern.MatchString(req.ID) {
		httpError(w, http.StatusBadRequest, "bad entry id")
		return
	}
	if err := req.check(); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if existing, err := s.store.Entries(r.Context(), pc.ID); err == nil && len(existing) >= 50 {
		httpError(w, http.StatusBadRequest, "a PC can have up to 50 entries")
		return
	}
	e := &store.Entry{ID: req.ID, PCID: pc.ID, Schedule: req.Schedule, Config: req.Config}
	if err := s.store.CreateEntry(r.Context(), e); err != nil {
		internalError(w, r, err)
		return
	}
	s.entriesChanged(r, pc.ID)
	writeJSON(w, http.StatusOK, entryJSON{ID: e.ID, Schedule: e.Schedule, Config: e.Config, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt})
}

func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	var req entryRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := req.check(); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	e := &store.Entry{ID: r.PathValue("entry"), PCID: pc.ID, Schedule: req.Schedule, Config: req.Config}
	switch err := s.store.UpdateEntry(r.Context(), e); {
	case isNotFound(err):
		httpError(w, http.StatusNotFound, "no such entry")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}
	s.entriesChanged(r, pc.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	switch err := s.store.DeleteEntry(r.Context(), pc.ID, r.PathValue("entry")); {
	case isNotFound(err):
		httpError(w, http.StatusNotFound, "no such entry")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}
	s.entriesChanged(r, pc.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleRunNow asks the PC to back up one entry (or all) right away.
func (s *Server) handleRunNow(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	var req struct {
		Entry string `json:"entry"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.hub.send(r.Context(), pc.ID, proto.Message{Type: proto.TypeRun, Entry: req.Entry}); err != nil {
		httpError(w, http.StatusConflict, pc.Name+" is offline right now; it backs up when it's back")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBrowse passes a locked folder listing request to the PC and its
// locked answer back. The server can read neither.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	var req struct {
		Data string `json:"data"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validSealed(req.Data) || len(req.Data) > 16*1024 {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	data, err := s.hub.ask(r.Context(), pc.ID, proto.Message{Type: proto.TypeBrowse, Data: req.Data})
	switch {
	case errors.Is(err, errOffline):
		httpError(w, http.StatusConflict, pc.Name+" is offline right now, so its folders can't be listed. You can type a path instead.")
	case err != nil:
		httpError(w, http.StatusGatewayTimeout, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"data": data})
	}
}

// handleAgentEntries gives the PC its entries.
func (s *Server) handleAgentEntries(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.Entries(r.Context(), pcFrom(r.Context()).ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := []proto.Entry{}
	for _, e := range entries {
		out = append(out, proto.Entry{ID: e.ID, Schedule: e.Schedule, Config: e.Config, UpdatedAt: e.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

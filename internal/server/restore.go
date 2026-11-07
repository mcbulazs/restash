package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/store"
)

// restoreJob is what the server knows about a restore: its progress, and a
// locked message from the PC when it ends. Kept in memory only.
type restoreJob struct {
	PCID    string    `json:"-"`
	UserID  string    `json:"-"`
	EventID int64     `json:"-"`
	ID      string    `json:"id"`
	State   string    `json:"state"` // sent, running, done, failed
	Percent float64   `json:"percent"`
	Files   int64     `json:"files"`
	Bytes   int64     `json:"bytes"`
	Detail  string    `json:"detail,omitempty"` // locked with the PC's entry key
	Updated time.Time `json:"updated"`
}

type restoreJobs struct {
	mu   sync.Mutex
	jobs map[string]*restoreJob
}

func (j *restoreJobs) put(job *restoreJob) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.jobs == nil {
		j.jobs = map[string]*restoreJob{}
	}
	for id, old := range j.jobs {
		if time.Since(old.Updated) > 24*time.Hour {
			delete(j.jobs, id)
		}
	}
	j.jobs[job.ID] = job
}

func (j *restoreJobs) get(id string) (restoreJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	if !ok {
		return restoreJob{}, false
	}
	return *job, true
}

// update applies a status from a PC to one of its own jobs, and returns
// the job if it just ended.
func (j *restoreJobs) update(pcID string, m proto.Message) *restoreJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[m.ID]
	if !ok || job.PCID != pcID {
		return nil
	}
	wasOpen := job.State != "done" && job.State != "failed"
	switch m.State {
	case "running", "done", "failed":
		job.State = m.State
	}
	job.Percent, job.Files, job.Bytes = m.Percent, m.Files, m.Bytes
	if m.Data != "" && len(m.Data) < 16*1024 && validSealed(m.Data) {
		job.Detail = m.Data
	}
	job.Updated = time.Now()
	if wasOpen && (job.State == "done" || job.State == "failed") {
		c := *job
		return &c
	}
	return nil
}

// handleRestore passes a locked restore job to the target PC.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
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
	if !validSealed(req.Data) || len(req.Data) > 48*1024 {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	b := make([]byte, 12)
	rand.Read(b)
	job := &restoreJob{PCID: pc.ID, ID: hex.EncodeToString(b), State: "sent", Updated: time.Now()}
	if err := s.hub.send(r.Context(), pc.ID, proto.Message{Type: proto.TypeRestore, ID: job.ID, Data: req.Data}); err != nil {
		httpError(w, http.StatusConflict, pc.Name+" is offline right now. Restoring to it works once it's back; downloading works anytime.")
		return
	}
	me := userFrom(r.Context())
	job.UserID = me.ID
	job.EventID, _ = s.store.AddEvent(r.Context(), store.Event{UserID: me.ID, PCID: pc.ID, Kind: "restore",
		Info: map[string]any{"pc": pc.Name, "state": "sent", "from": clean(r.URL.Query().Get("from"), 60)}})
	s.restores.put(job)
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRestoreStatus(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	job, ok := s.restores.get(r.PathValue("job"))
	if !ok || job.PCID != pc.ID {
		httpError(w, http.StatusNotFound, "no such restore")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleAgentPCs lists the owner's other PCs, so a PC can bring back
// another one's files. Their keys are locked with the account key, which
// only the owner's password opens.
func (s *Server) handleAgentPCs(w http.ResponseWriter, r *http.Request) {
	me := pcFrom(r.Context())
	pcs, err := s.store.PCs(r.Context(), me.UserID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	_, lastOK, err := s.store.LastBackups(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := []proto.OtherPC{}
	for _, p := range pcs {
		if p.ID == me.ID || p.PCKey == "" {
			continue
		}
		o := proto.OtherPC{ID: p.ID, Name: p.Name, OS: p.OS, PCKey: p.PCKey, Stored: s.sizes.size(p.ID)}
		if t, ok := lastOK[p.ID]; ok {
			o.LastBackup = &t
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
}

// finishRestoreEvent updates the activity line of a restore that ended.
func (s *Server) finishRestoreEvent(job *restoreJob, pcName string) {
	info := map[string]any{"pc": pcName, "state": job.State, "files": job.Files, "bytes": job.Bytes}
	if evs, err := s.store.Events(context.Background(), job.UserID, time.Now().Add(-48*time.Hour)); err == nil {
		for _, e := range evs {
			if e.ID == job.EventID && e.Info["from"] != nil {
				info["from"] = e.Info["from"]
			}
		}
	}
	s.store.UpdateEventInfo(context.Background(), job.EventID, info)
}

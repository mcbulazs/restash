package server

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
)

// Live backup progress, as the agents report it. Kept in memory only: it
// matters while a backup runs, and the finished run is stored as usual.

type progressJSON struct {
	Entry      string    `json:"entry"`
	Percent    float64   `json:"percent"` // 0 to 1
	Files      int64     `json:"files"`
	TotalFiles int64     `json:"totalFiles"`
	Bytes      int64     `json:"bytes"`
	TotalBytes int64     `json:"totalBytes"`
	ETA        int64     `json:"eta"` // seconds left, 0 if unknown
	StartedAt  time.Time `json:"startedAt"`
	updated    time.Time
}

// progressStale drops a backup that stopped reporting (a lost message).
const progressStale = 2 * time.Minute

type progressStore struct {
	mu sync.Mutex
	m  map[string]map[string]*progressJSON // pc -> entry -> progress
}

func (ps *progressStore) update(pcID string, msg proto.Message, now time.Time) {
	if msg.Entry == "" || len(msg.Entry) > 64 {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.m == nil {
		ps.m = map[string]map[string]*progressJSON{}
	}
	if msg.State != "running" {
		delete(ps.m[pcID], msg.Entry)
		return
	}
	byEntry := ps.m[pcID]
	if byEntry == nil {
		byEntry = map[string]*progressJSON{}
		ps.m[pcID] = byEntry
	}
	p := byEntry[msg.Entry]
	if p == nil {
		if len(byEntry) >= 50 {
			return
		}
		p = &progressJSON{Entry: msg.Entry, StartedAt: now}
		byEntry[msg.Entry] = p
	}
	p.Percent = min(max(msg.Percent, 0), 1)
	p.Files, p.TotalFiles = max(msg.Files, 0), max(msg.TotalFiles, 0)
	p.Bytes, p.TotalBytes = max(msg.Bytes, 0), max(msg.TotalBytes, 0)
	p.ETA = max(msg.ETA, 0)
	p.updated = now
}

// clear forgets a PC's backups (it went offline).
func (ps *progressStore) clear(pcID string) {
	ps.mu.Lock()
	delete(ps.m, pcID)
	ps.mu.Unlock()
}

// list is what's running on a PC now, oldest first.
func (ps *progressStore) list(pcID string, now time.Time) []progressJSON {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := []progressJSON{}
	for id, p := range ps.m[pcID] {
		if now.Sub(p.updated) > progressStale {
			delete(ps.m[pcID], id)
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// handleProgress is polled by the PC page while a backup runs.
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	pc := s.ownPC(w, r)
	if pc == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"online": s.hub.online(pc.ID), "running": s.progress.list(pc.ID, s.now())})
}

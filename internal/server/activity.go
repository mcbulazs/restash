package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/status"
	"github.com/mcbulazs/restash/internal/store"
)

// activityItem is one line in the activity feed.
type activityItem struct {
	Kind    string         `json:"kind"` // backups, failed, late, quiet, recovered, restore, download, login
	At      time.Time      `json:"at"`
	PCID    string         `json:"pcId,omitempty"`
	PC      string         `json:"pc,omitempty"`
	UserID  string         `json:"userId"`
	User    string         `json:"user"`
	Mine    bool           `json:"mine"`
	Entries []string       `json:"entries,omitempty"` // entry ids (names are locked)
	Count   int            `json:"count,omitempty"`
	Added   int64          `json:"added,omitempty"`
	Detail  string         `json:"detail,omitempty"` // locked with the PC's entry key
	Info    map[string]any `json:"info,omitempty"`
	Secret  string         `json:"secret,omitempty"` // locked with the owner's account key
	PCKey   string         `json:"pcKey,omitempty"`  // so the owner can open Detail
}

// A run within this long of the previous one on the same PC is shown as
// part of the same line.
const burst = 15 * time.Minute

// handleActivity is the feed: everything for the admin (names and times
// only), one's own things for everyone else.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := userFrom(ctx)
	days := 30
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	scope := me.ID
	if me.Admin {
		scope = ""
	}
	users, err := s.store.Users(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	names := map[string]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	pcs, err := s.store.PCs(ctx, scope)
	if err != nil {
		internalError(w, r, err)
		return
	}
	pcByID := map[string]store.PC{}
	for _, p := range pcs {
		pcByID[p.ID] = p
	}
	recent, err := s.store.BackupsSince(ctx, since)
	if err != nil {
		internalError(w, r, err)
		return
	}
	var items []activityItem
	base := func(p store.PC) activityItem {
		it := activityItem{PCID: p.ID, PC: p.Name, UserID: p.UserID, User: names[p.UserID], Mine: p.UserID == me.ID}
		if it.Mine {
			it.PCKey = p.PCKey
		}
		return it
	}
	for pcID, runs := range recent {
		p, ok := pcByID[pcID]
		if !ok {
			continue
		}
		var cur *activityItem
		for _, b := range runs { // oldest first
			if !b.OK {
				it := base(p)
				it.Kind, it.At, it.Entries = "failed", b.FinishedAt, []string{b.Entry}
				if it.Mine {
					it.Detail = b.Detail
				}
				items = append(items, it)
				cur = nil
				continue
			}
			if cur == nil || b.FinishedAt.Sub(cur.At) > burst {
				it := base(p)
				it.Kind = "backups"
				items = append(items, it)
				cur = &items[len(items)-1]
			}
			cur.At = b.FinishedAt
			cur.Count++
			cur.Added += b.Added
			if !contains(cur.Entries, b.Entry) {
				cur.Entries = append(cur.Entries, b.Entry)
			}
			if !cur.Mine {
				cur.Entries = nil // others see counts only
			}
		}
	}
	events, err := s.store.Events(ctx, scope, since)
	if err != nil {
		internalError(w, r, err)
		return
	}
	for _, e := range events {
		it := activityItem{Kind: e.Kind, At: e.At, UserID: e.UserID, User: names[e.UserID], Mine: e.UserID == me.ID, Info: e.Info}
		if p, ok := pcByID[e.PCID]; ok {
			it.PCID, it.PC = p.ID, p.Name
		}
		if it.Mine {
			it.Secret = e.Secret
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	if items == nil {
		items = []activityItem{}
	}
	backups, problems, _ := s.weekCounts(ctx, scope)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "weekBackups": backups, "weekProblems": problems})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// weekCounts counts the last 7 days for a person ("" for everyone):
// successful backups, problems (failed runs plus late and quiet spells),
// and PCs.
func (s *Server) weekCounts(ctx context.Context, scope string) (backups, problems, pcCount int) {
	since := s.now().Add(-7 * 24 * time.Hour)
	pcs, err := s.store.PCs(ctx, scope)
	if err != nil {
		return
	}
	mine := map[string]bool{}
	for _, p := range pcs {
		mine[p.ID] = true
	}
	pcCount = len(pcs)
	recent, _ := s.store.BackupsSince(ctx, since)
	for pcID, runs := range recent {
		if !mine[pcID] {
			continue
		}
		for _, b := range runs {
			if b.OK {
				backups++
			} else {
				problems++
			}
		}
	}
	events, _ := s.store.Events(ctx, scope, since)
	for _, e := range events {
		if e.Kind == status.Late || e.Kind == status.Quiet {
			problems++
		}
	}
	return
}

// handleDownloaded records a download made in the browser. What was
// downloaded is locked with the person's account key.
func (s *Server) handleDownloaded(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PCID   string `json:"pcId"`
		Items  int    `json:"items"`
		Bytes  int64  `json:"bytes"`
		Secret string `json:"secret"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	me := userFrom(r.Context())
	pc, err := s.store.PCByID(r.Context(), req.PCID)
	if err != nil || pc.UserID != me.ID {
		httpError(w, http.StatusNotFound, "no such PC")
		return
	}
	if req.Secret != "" && (len(req.Secret) > 8*1024 || !validSealed(req.Secret)) {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	s.store.AddEvent(r.Context(), store.Event{UserID: me.ID, PCID: pc.ID, Kind: "download", Secret: req.Secret,
		Info: map[string]any{"pc": pc.Name, "items": req.Items, "bytes": req.Bytes, "browser": browserName(r.UserAgent())}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetNotify(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.NotifySettings(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": n, "server": s.notify.server, "admin": userFrom(r.Context()).Admin})
}

func (s *Server) handleSetNotify(w http.ResponseWriter, r *http.Request) {
	var n store.Notify
	if !readJSON(w, r, &n) {
		return
	}
	n.Topic = strings.TrimSpace(n.Topic)
	if n.Topic != "" && !topicPattern.MatchString(n.Topic) {
		httpError(w, http.StatusBadRequest, "a topic name uses only letters, digits, - and _ (up to 64)")
		return
	}
	if err := s.store.SetNotifySettings(r.Context(), userFrom(r.Context()).ID, n); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if !s.limits.allow("ntfytest:"+u.ID, 5, 10*time.Minute) {
		httpError(w, http.StatusTooManyRequests, "that's enough tests for now")
		return
	}
	n, err := s.store.NotifySettings(r.Context(), u.ID)
	if err != nil || n.Topic == "" {
		httpError(w, http.StatusBadRequest, "set a topic first")
		return
	}
	if err := s.notify.send(r.Context(), n.Topic, message{Title: "restash works", Body: "Alerts for " + u.Name + " will show up here.", Tags: "tada"}); err != nil {
		httpError(w, http.StatusBadGateway, "ntfy didn't take it: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// browserName turns a user agent into "Firefox on Linux".
func browserName(ua string) string {
	b := "A browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		b = "Edge"
	case strings.Contains(ua, "Firefox/"):
		b = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		b = "Chrome"
	case strings.Contains(ua, "Safari/"):
		b = "Safari"
	}
	o := ""
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		o = "iOS"
	case strings.Contains(ua, "Android"):
		o = "Android"
	case strings.Contains(ua, "Windows"):
		o = "Windows"
	case strings.Contains(ua, "Mac OS"):
		o = "macOS"
	case strings.Contains(ua, "Linux"):
		o = "Linux"
	}
	if o == "" {
		return b
	}
	return b + " on " + o
}

const deviceCookie = "restash_device"

// noteDevice remembers this browser for the person and, if it's new and
// this isn't their first login, records it and tells them.
func (s *Server) noteDevice(w http.ResponseWriter, r *http.Request, u *store.User, quiet bool) {
	dev := ""
	if c, err := r.Cookie(deviceCookie); err == nil && len(c.Value) >= 32 && len(c.Value) <= 64 {
		dev = c.Value
	} else {
		dev = store.NewToken()
		http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: dev, Path: "/", MaxAge: 2 * 365 * 24 * 3600,
			HttpOnly: true, Secure: s.cfg.PublicURL.Scheme == "https", SameSite: http.SameSiteLaxMode})
	}
	isNew, err := s.store.SeenDevice(r.Context(), u.ID, dev)
	if err != nil || !isNew || quiet {
		return
	}
	browser := browserName(r.UserAgent())
	s.store.AddEvent(r.Context(), store.Event{UserID: u.ID, Kind: "login", Info: map[string]any{"browser": browser}})
	go s.notify.tell(context.Background(), u.ID, message{
		Title: "New login for " + u.Name, Body: browser + ". If this wasn't " + u.Name + ", change the password.", Tags: "key", Kind: "login",
	})
}

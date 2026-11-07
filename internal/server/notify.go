package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/status"
	"github.com/mcbulazs/restash/internal/store"
)

// ntfy topics: letters, digits, - and _, as ntfy allows.
var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// message is one notification.
type message struct {
	Title    string
	Body     string
	Tags     string // ntfy emoji tags, e.g. "warning"
	Priority string // ntfy priority 1-5
	Kind     string // fail, quiet, login, weekly: which setting it falls under
}

// notifier sends messages to people's ntfy topics. The server address is
// fixed by the admin; people only pick a topic name, so nobody can make the
// server send requests elsewhere.
type notifier struct {
	server string // e.g. https://ntfy.sh
	client *http.Client
	store  *store.Store
	site   string // link included in messages
}

func (n *notifier) send(ctx context.Context, topic string, m message) error {
	if n.server == "" || !topicPattern.MatchString(topic) {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(n.server, "/")+"/"+url.PathEscape(topic), strings.NewReader(m.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", m.Title)
	if m.Tags != "" {
		req.Header.Set("Tags", m.Tags)
	}
	if m.Priority != "" {
		req.Header.Set("Priority", m.Priority)
	}
	if n.site != "" {
		req.Header.Set("Click", n.site)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy said %s", resp.Status)
	}
	return nil
}

func wants(n store.Notify, kind string) bool {
	switch kind {
	case "fail":
		return n.Fail
	case "quiet":
		return n.Quiet
	case "login":
		return n.Login
	case "weekly":
		return n.Weekly
	}
	return false
}

// tell sends a message about something of ownerID's to them and to the
// admins who want it, each at most once.
func (n *notifier) tell(ctx context.Context, ownerID string, m message) {
	users, err := n.store.Users(ctx)
	if err != nil {
		slog.Error("notify: listing users", "err", err)
		return
	}
	sent := map[string]bool{}
	for _, u := range users {
		if u.ID != ownerID && !u.Admin {
			continue
		}
		prefs, err := n.store.NotifySettings(ctx, u.ID)
		if err != nil || prefs.Topic == "" || !wants(prefs, m.Kind) || sent[prefs.Topic] {
			continue
		}
		sent[prefs.Topic] = true
		if err := n.send(ctx, prefs.Topic, m); err != nil {
			slog.Warn("notify: sending failed", "user", u.ID, "err", err)
		}
	}
}

// checker looks for problems every few minutes (and when poked), records
// new ones as events and announces them once, and announces recoveries.
func (s *Server) runChecker(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.check(ctx); err != nil {
			slog.Error("checking backups", "err", err)
		}
		s.weeklySummaries(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.pokeCheck:
		}
	}
}

func (s *Server) poke() {
	select {
	case s.pokeCheck <- struct{}{}:
	default:
	}
}

// statuses evaluates every PC.
func (s *Server) statuses(ctx context.Context) (map[string]status.PC, []store.PC, error) {
	pcs, err := s.store.PCs(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	entries, err := s.store.AllEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	runs, err := s.store.EntryRuns(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	out := map[string]status.PC{}
	for _, p := range pcs {
		if s.hub.online(p.ID) {
			seen := now
			p.LastSeen = &seen
		}
		out[p.ID] = status.Evaluate(p, entries[p.ID], runs[p.ID], now)
	}
	return out, pcs, nil
}

func (s *Server) check(ctx context.Context) error {
	states, pcs, err := s.statuses(ctx)
	if err != nil {
		return err
	}
	known, err := s.store.Alerts(ctx)
	if err != nil {
		return err
	}
	byID := map[string]store.PC{}
	for _, p := range pcs {
		byID[p.ID] = p
	}
	current := map[string]bool{}
	for _, st := range states {
		for _, p := range st.Problems {
			key := p.Key()
			current[key] = true
			if _, ok := known[key]; ok {
				continue
			}
			pc := byID[p.PCID]
			s.store.StartAlert(ctx, store.Alert{Key: key, UserID: pc.UserID, Started: s.now()})
			s.announce(ctx, pc, p)
		}
	}
	for key, a := range known {
		if current[key] {
			continue
		}
		s.store.EndAlert(ctx, key)
		kind, rest, _ := strings.Cut(key, ":")
		pcID, _, _ := strings.Cut(rest, ":")
		pc, ok := byID[pcID]
		if !ok || kind == status.Late {
			continue // late ones clear quietly when the backup runs
		}
		s.store.AddEvent(ctx, store.Event{UserID: a.UserID, PCID: pc.ID, Kind: "recovered", Info: map[string]any{"pc": pc.Name, "was": kind}})
		s.notify.tell(ctx, a.UserID, message{
			Title: pc.Name + " is backing up again", Body: "The problem from " + a.Started.Local().Format("Jan 2") + " is gone.",
			Tags: "white_check_mark", Kind: map[string]string{status.Failed: "fail", status.Quiet: "quiet"}[kind],
		})
	}
	return nil
}

// announce records and sends a new problem. Messages name the PC only:
// entry names are locked and the server can't read them.
func (s *Server) announce(ctx context.Context, pc store.PC, p status.Problem) {
	info := map[string]any{"pc": pc.Name, "entry": p.Entry, "since": p.Since}
	var m message
	switch p.Kind {
	case status.Failed:
		// Failures are already in the feed as failed runs.
		m = message{Title: "A backup failed on " + pc.Name, Body: "Open restash to see what went wrong.", Tags: "warning", Priority: "4", Kind: "fail"}
	case status.Late:
		s.store.AddEvent(ctx, store.Event{UserID: pc.UserID, PCID: pc.ID, Kind: "late", Info: info})
		m = message{Title: "A backup on " + pc.Name + " is late", Body: "It was due " + p.Since.Local().Format("Jan 2 at 15:04") + " and hasn't run, though the PC was on.", Tags: "hourglass", Kind: "fail"}
	case status.Quiet:
		days := int(s.now().Sub(p.Since).Hours() / 24)
		info["days"] = days
		s.store.AddEvent(ctx, store.Event{UserID: pc.UserID, PCID: pc.ID, Kind: "quiet", Info: info})
		last := "It was last seen " + "a while ago."
		if pc.LastSeen != nil {
			last = "It was last seen " + pc.LastSeen.Local().Format("Jan 2") + ". It may be switched off or offline."
		}
		m = message{Title: fmt.Sprintf("%s has been quiet for %d days", pc.Name, days), Body: last, Tags: "zzz", Priority: "4", Kind: "quiet"}
	}
	s.notify.tell(ctx, pc.UserID, m)
}

// weeklySummaries sends Monday-morning summaries to those who want them.
func (s *Server) weeklySummaries(ctx context.Context) {
	now := s.now().Local()
	if now.Weekday() != time.Monday || now.Hour() < 8 {
		return
	}
	week := now.Format("2006-01-02")
	users, err := s.store.Users(ctx)
	if err != nil {
		return
	}
	for _, u := range users {
		prefs, err := s.store.NotifySettings(ctx, u.ID)
		if err != nil || !prefs.Weekly || prefs.Topic == "" {
			continue
		}
		if !s.store.Once(ctx, "weekly:"+u.ID, week) {
			continue
		}
		scope := u.ID
		if u.Admin {
			scope = ""
		}
		backups, problems, pcs := s.weekCounts(ctx, scope)
		body := fmt.Sprintf("Last week: %d backups across %d PCs, %d problem%s.", backups, pcs, problems, map[bool]string{true: "", false: "s"}[problems == 1])
		if err := s.notify.send(ctx, prefs.Topic, message{Title: "restash weekly summary", Body: body, Tags: "calendar"}); err != nil {
			slog.Warn("weekly summary", "err", err)
		}
	}
}

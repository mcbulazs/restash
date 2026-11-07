package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/restic"
	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/vault"
)

// LocalEntry is an entry with its settings unlocked.
type LocalEntry struct {
	ID       string
	Schedule schedule.Schedule
	proto.EntryConfig
}

// Runner keeps this PC's entries and backs them up on schedule, one job at
// a time.
type Runner struct {
	cfg       *Config
	client    *Client
	restic    *restic.Restic
	entryKey  []byte
	statePath string
	now       func() time.Time

	mu      sync.Mutex
	entries []LocalEntry
	state   State
	queued  map[string]bool
	jobs    chan string // entry ids
	wake    chan struct{}
	busy    atomic.Int32        // backups and restores running
	send    func(proto.Message) // the server link, nil while offline
}

// SetLink gives the runner the server link, for live progress.
func (rn *Runner) SetLink(send func(proto.Message)) {
	rn.mu.Lock()
	rn.send = send
	rn.mu.Unlock()
}

// tell sends m to the server if linked; progress is best effort.
func (rn *Runner) tell(m proto.Message) {
	rn.mu.Lock()
	send := rn.send
	rn.mu.Unlock()
	if send != nil {
		send(m)
	}
}

// Busy says whether a backup or restore is running (updates wait for it).
func (rn *Runner) Busy() bool { return rn.busy.Load() > 0 }

// NewRunner builds a runner for an unlocked PC.
func NewRunner(cfg *Config, statePath string, r *restic.Restic) (*Runner, error) {
	pcKey, err := vault.PCKeyFromPassword(cfg.RepoKey)
	if err != nil {
		return nil, err
	}
	st, err := LoadState(statePath)
	if err != nil {
		return nil, err
	}
	return &Runner{
		cfg: cfg, client: NewClient(cfg.Server, cfg.PCID, cfg.Token), restic: r,
		entryKey: vault.EntryKey(pcKey), statePath: statePath, now: time.Now,
		state: st, queued: map[string]bool{}, jobs: make(chan string, 100), wake: make(chan struct{}, 1),
	}, nil
}

// Entries returns the current entries.
func (rn *Runner) Entries() []LocalEntry {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return append([]LocalEntry{}, rn.entries...)
}

// LastRun is when an entry last ran (zero if never).
func (rn *Runner) LastRun(id string) time.Time {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.state.LastRun[id]
}

// Sync fetches the entries from the server and unlocks them. Entries that
// can't be unlocked are skipped and logged.
func (rn *Runner) Sync(ctx context.Context) error {
	list, err := rn.client.Entries(ctx)
	if err != nil {
		return err
	}
	var out []LocalEntry
	for _, e := range list {
		plain, err := vault.Open(rn.entryKey, e.Config, vault.LabelEntry(rn.cfg.PCID, e.ID))
		if err != nil {
			slog.Error("can't unlock an entry's settings; skipping it", "entry", e.ID)
			continue
		}
		le := LocalEntry{ID: e.ID, Schedule: e.Schedule}
		if err := json.Unmarshal(plain, &le.EntryConfig); err != nil {
			slog.Error("entry settings are damaged; skipping it", "entry", e.ID)
			continue
		}
		out = append(out, le)
	}
	rn.mu.Lock()
	rn.entries = out
	rn.mu.Unlock()
	slog.Info("entries up to date", "count", len(out))
	rn.poke()
	return nil
}

func (rn *Runner) poke() {
	select {
	case rn.wake <- struct{}{}:
	default:
	}
}

// RunNow queues one entry, or all of them if id is "".
func (rn *Runner) RunNow(id string) {
	for _, e := range rn.Entries() {
		if id == "" || e.ID == id {
			rn.enqueue(e.ID)
		}
	}
}

func (rn *Runner) enqueue(id string) {
	rn.mu.Lock()
	if rn.queued[id] {
		rn.mu.Unlock()
		return
	}
	rn.queued[id] = true
	rn.mu.Unlock()
	rn.jobs <- id
}

// Run checks schedules every minute (and whenever entries change) and works
// through the queue until ctx is cancelled.
func (rn *Runner) Run(ctx context.Context) {
	go rn.work(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		now := rn.now()
		for _, e := range rn.Entries() {
			if e.Schedule.Due(rn.LastRun(e.ID), now) {
				rn.enqueue(e.ID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-rn.wake:
		}
	}
}

func (rn *Runner) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-rn.jobs:
			rn.runEntry(ctx, id)
			rn.mu.Lock()
			delete(rn.queued, id)
			rn.mu.Unlock()
			rn.maybePrune(ctx)
		}
	}
}

func (rn *Runner) entry(id string) (LocalEntry, bool) {
	for _, e := range rn.Entries() {
		if e.ID == id {
			return e, true
		}
	}
	return LocalEntry{}, false
}

// tail keeps the last few KB written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8192 {
		t.buf = t.buf[len(t.buf)-8192:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// runEntry backs up one entry, reports the result and applies its retention.
func (rn *Runner) runEntry(ctx context.Context, id string) {
	e, ok := rn.entry(id)
	if !ok {
		return
	}
	rn.busy.Add(1)
	defer rn.busy.Add(-1)
	slog.Info("backing up", "entry", e.ID)
	started := rn.now()
	errs := &tail{}
	r := *rn.restic
	r.Stdout, r.Stderr = nil, errs
	var sum *restic.Summary
	var err error
	// Over the storage limit: don't upload, say why. (If the server can't be
	// asked, back up anyway; the server has the last word.)
	limited := ""
	qctx, qcancel := context.WithTimeout(ctx, 30*time.Second)
	st, qerr := rn.client.Storage(qctx)
	qcancel()
	if qerr == nil && st.Limit > 0 && st.Used >= st.Limit {
		limited = LimitMessage(st)
		slog.Warn("storage limit reached; not backing up", "entry", e.ID, "used", st.Used, "limit", st.Limit)
	} else {
		rn.tell(proto.Message{Type: proto.TypeBackupState, Entry: e.ID, State: "running"})
		var lastSent time.Time
		progress := func(p restic.BackupProgress) {
			if time.Since(lastSent) < 2*time.Second {
				return
			}
			lastSent = time.Now()
			rn.tell(proto.Message{Type: proto.TypeBackupState, Entry: e.ID, State: "running",
				Percent: p.Percent, Files: p.FilesDone, TotalFiles: p.TotalFiles, Bytes: p.BytesDone, TotalBytes: p.TotalBytes, ETA: p.SecondsRemaining})
		}
		sum, err = r.Backup(ctx, restic.BackupOptions{Paths: e.Paths, Tags: []string{"entry:" + e.ID}, Excludes: e.Skips, Progress: progress})
		if ctx.Err() != nil {
			return // shutting down; try again next time
		}
	}

	report := proto.BackupReport{Entry: e.ID, StartedAt: started, FinishedAt: rn.now(), OK: sum != nil}
	var detail string
	switch {
	case sum != nil:
		report.Files, report.Bytes, report.Added, report.Snapshot = int64(sum.TotalFilesProcessed), sum.TotalBytesProcessed, sum.DataAddedPacked, sum.SnapshotID
		if errors.Is(err, restic.ErrIncomplete) {
			detail = "Some files couldn't be read and were skipped:\n" + strings.Join(firstN(restic.Messages(errs.String()), 10), "\n")
		}
		slog.Info("backup done", "entry", e.ID, "files", sum.TotalFilesProcessed, "added", sum.DataAddedPacked)
	case limited != "":
		detail = limited
	default:
		msgs := restic.Messages(errs.String())
		if len(msgs) == 0 && err != nil {
			msgs = []string{err.Error()}
		}
		detail = strings.Join(lastN(msgs, 10), "\n")
		slog.Error("backup failed", "entry", e.ID, "err", err)
	}
	if detail != "" {
		if sealed, err := vault.Seal(rn.entryKey, []byte(detail), vault.LabelRunDetail(rn.cfg.PCID)); err == nil {
			report.Detail = sealed
		}
	}
	rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := rn.client.ReportBackup(rctx, report); err != nil {
		slog.Warn("couldn't report the backup", "err", err)
	}
	cancel()
	// The report lists the run; this ends the live view.
	end := proto.Message{Type: proto.TypeBackupState, Entry: e.ID, State: "done", Percent: 1}
	if sum == nil {
		end.State = "failed"
	}
	rn.tell(end)

	rn.mu.Lock()
	rn.state.LastRun[e.ID] = started
	rn.mu.Unlock()

	// Over the limit, cleaning up old backups is how space comes back, so
	// it happens right away, not once a day.
	if sum != nil || limited != "" {
		rn.applyRetention(ctx, e)
	}
	if limited != "" {
		rn.prune(ctx, true)
	}
	rn.saveState()
}

// applyRetention removes this entry's old snapshots. Their data is cleaned
// up later by prune, which moves it to the server's trash.
func (rn *Runner) applyRetention(ctx context.Context, e LocalEntry) {
	if e.Retention.Validate() != nil {
		return // keep everything rather than guess
	}
	var out bytes.Buffer
	r := *rn.restic
	r.Stdout, r.Stderr = &out, &out
	args := append([]string{"forget", "--tag", "entry:" + e.ID, "--group-by", "tags"}, e.Retention.ForgetArgs()...)
	if err := r.Run(ctx, args...); err != nil {
		slog.Warn("applying retention failed", "entry", e.ID, "err", err)
		return
	}
	if strings.Contains(out.String(), "remove ") {
		rn.mu.Lock()
		rn.state.PruneNeeded = true
		rn.mu.Unlock()
	}
}

// PruneEvery is how often data of removed snapshots is cleaned up at most.
const PruneEvery = 24 * time.Hour

func (rn *Runner) maybePrune(ctx context.Context) {
	rn.mu.Lock()
	need := rn.state.PruneNeeded && rn.now().Sub(rn.state.LastPrune) >= PruneEvery && len(rn.queued) == 0
	rn.mu.Unlock()
	if !need {
		return
	}
	rn.prune(ctx, false)
}

// prune cleans up data of removed snapshots. tight is for when the owner is
// over their storage limit: only data nothing uses is deleted and nothing is
// rewritten, since the server may refuse new data then.
func (rn *Runner) prune(ctx context.Context, tight bool) {
	rn.mu.Lock()
	need := rn.state.PruneNeeded
	rn.mu.Unlock()
	if !need {
		return
	}
	slog.Info("cleaning up data of old backups", "tight", tight)
	r := *rn.restic
	r.Stdout, r.Stderr = nil, nil
	args := []string{"prune"}
	if tight {
		args = append(args, "--max-unused", "unlimited")
	}
	if err := r.Run(ctx, args...); err != nil {
		slog.Warn("clean-up failed; trying again later", "err", err)
		return
	}
	rn.mu.Lock()
	rn.state.PruneNeeded = false
	rn.state.LastPrune = rn.now()
	rn.mu.Unlock()
	rn.saveState()
}

func (rn *Runner) saveState() {
	rn.mu.Lock()
	st := rn.state
	rn.mu.Unlock()
	if err := st.Save(rn.statePath); err != nil {
		slog.Error("saving state", "err", err)
	}
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("…and %d more", len(s)-n))
	}
	return s
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// State is what the agent remembers between runs.
type State struct {
	LastRun     map[string]time.Time `json:"last_run"`
	LastPrune   time.Time            `json:"last_prune"`
	PruneNeeded bool                 `json:"prune_needed"`
}

// LoadState reads the state file; a missing file is an empty state.
func LoadState(path string) (State, error) {
	st := State{LastRun: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{LastRun: map[string]time.Time{}}, nil // start over rather than fail
	}
	if st.LastRun == nil {
		st.LastRun = map[string]time.Time{}
	}
	return st, nil
}

// Save writes the state file.
func (st State) Save(path string) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LimitMessage explains a backup skipped for the storage limit.
func LimitMessage(st proto.Storage) string {
	return fmt.Sprintf("Storage limit reached: %s of %s used, so nothing was backed up. "+
		"Ask the admin for more room, or free some by keeping fewer old backups or skipping folders.", gb(st.Used), gb(st.Limit))
}

// gb is a size like the website shows it (1 GB = 1000 MB).
func gb(b int64) string {
	switch {
	case b >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(b)/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	default:
		return fmt.Sprintf("%.0f MB", float64(b)/1e6)
	}
}

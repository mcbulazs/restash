package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/restic"
	"github.com/mcbulazs/restash/internal/resticread"
	"github.com/mcbulazs/restash/internal/vault"
)

// RestoreStatus reports how a restore is going. detail is shown to the
// owner (locked first by the caller).
type RestoreStatus func(state string, p restic.RestoreProgress, detail string)

var restoreMu sync.Mutex // one restore at a time

// sourceRestic is restic set up for the repo a job restores from: this
// PC's own, or (read-only) another PC's of the same owner.
func (rn *Runner) sourceRestic(job proto.RestoreJob) (*restic.Restic, bool) {
	r := *rn.restic
	if job.SourcePC == "" || job.SourcePC == rn.cfg.PCID {
		return &r, false
	}
	r.Repo.URL = strings.TrimSuffix(rn.cfg.Server, "/") + "/repo/" + job.SourcePC + "/"
	r.Repo.Key = job.SourceKey
	return &r, true
}

// Restore carries out a restore job.
func (rn *Runner) Restore(ctx context.Context, job proto.RestoreJob, status RestoreStatus) {
	restoreMu.Lock()
	defer restoreMu.Unlock()
	where, err := rn.restore(ctx, job, status)
	if err != nil {
		status("failed", restic.RestoreProgress{}, err.Error())
		return
	}
	status("done", restic.RestoreProgress{Percent: 1}, "Restored to "+where)
}

func (rn *Runner) restore(ctx context.Context, job proto.RestoreJob, status RestoreStatus) (string, error) {
	if len(job.Names) == 0 {
		return "", errors.New("nothing was picked to restore")
	}
	for _, n := range job.Names {
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\`) {
			return "", fmt.Errorf("bad name %q", n)
		}
	}
	r, other := rn.sourceRestic(job)
	errs := &tail{}
	r.Stdout, r.Stderr = nil, errs
	snap := job.Snapshot
	if job.Dir != "" && job.Dir != "/" {
		snap += ":" + job.Dir
	}
	includes := make([]string, len(job.Names))
	for i, n := range job.Names {
		includes[i] = "/" + n
	}
	progress := func(p restic.RestoreProgress) { status("running", p, "") }
	fail := func(err error) error {
		if msgs := restic.Messages(errs.String()); len(msgs) > 0 {
			return errors.New(strings.Join(lastN(msgs, 8), "\n"))
		}
		return err
	}

	switch job.Mode {
	case "folder":
		if job.Target == "" || !filepath.IsAbs(job.Target) {
			return "", errors.New("pick a folder on this PC to restore into")
		}
		dest := uniquePath(filepath.Join(job.Target, capitalize(job.Label)))
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return "", err
		}
		if err := r.Restore(ctx, snap, dest, includes, other, progress); err != nil {
			return "", fail(err)
		}
		return dest, nil

	case "originals":
		dir := job.OriginalDir
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s isn't a folder on this PC; restore into a folder instead", dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		// Restore next to the originals into a hidden folder first, then
		// move each item into place under a new name. Same folder, so the
		// moves are renames and nothing existing is touched.
		tmp, err := os.MkdirTemp(dir, ".restash-restore-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := r.Restore(ctx, snap, tmp, includes, other, progress); err != nil {
			return "", fail(err)
		}
		var placed []string
		for _, n := range job.Names {
			src := filepath.Join(tmp, n)
			if _, err := os.Lstat(src); err != nil {
				continue
			}
			dst := uniquePath(filepath.Join(dir, withLabel(n, job.Label, isDir(src))))
			if err := os.Rename(src, dst); err != nil {
				return "", err
			}
			placed = append(placed, filepath.Base(dst))
		}
		if len(placed) == 0 {
			return "", errors.New("nothing was restored")
		}
		return dir + " (" + strings.Join(placed, ", ") + ")", nil
	}
	return "", fmt.Errorf("unknown restore mode %q", job.Mode)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// withLabel turns "thesis.docx" into "thesis (restored Nov 7).docx".
func withLabel(name, label string, dir bool) string {
	if label == "" {
		label = "restored"
	}
	ext := filepath.Ext(name)
	if dir || ext == name {
		ext = ""
	}
	return strings.TrimSuffix(name, ext) + " (" + label + ")" + ext
}

// uniquePath adds " 2", " 3"... before the extension until nothing is there.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	if isDir(p) {
		ext = ""
	}
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s %d%s", base, i, ext)
		if _, err := os.Lstat(c); os.IsNotExist(err) {
			return c
		}
	}
}

func capitalize(s string) string {
	if s == "" {
		return "Restored"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// HandleRestore unlocks a restore job from the server, runs it, and sends
// locked progress back through send.
func (rn *Runner) HandleRestore(ctx context.Context, id, data string, send func(proto.Message)) {
	label := vault.LabelRestore(rn.cfg.PCID)
	plain, err := vault.Open(rn.entryKey, data, label)
	if err != nil {
		return // not for us
	}
	var job proto.RestoreJob
	if err := json.Unmarshal(plain, &job); err != nil {
		return
	}
	last := time.Time{}
	rn.Restore(ctx, job, func(state string, p restic.RestoreProgress, detail string) {
		if state == "running" && time.Since(last) < 2*time.Second {
			return
		}
		last = time.Now()
		m := proto.Message{Type: proto.TypeRestoreState, ID: id, State: state, Percent: p.Percent, Files: p.FilesDone, Bytes: p.BytesDone}
		if detail != "" {
			m.Data, _ = vault.Seal(rn.entryKey, []byte(detail), label)
		}
		send(m)
	})
}

// PullPlan is what bringing back another PC's backup will restore: the
// newest snapshot of each of its entries.
type PullPlan struct {
	Snapshots []restic.Snapshot
}

// PlanPull finds the newest snapshot of each entry in another PC's repo.
func (rn *Runner) PlanPull(ctx context.Context, sourcePC, sourceKey string) (*PullPlan, error) {
	r, _ := rn.sourceRestic(proto.RestoreJob{SourcePC: sourcePC, SourceKey: sourceKey})
	r.Stdout, r.Stderr = nil, nil
	list, err := r.Snapshots(ctx, true)
	if err != nil {
		return nil, err
	}
	newest := map[string]restic.Snapshot{}
	var order []string
	for _, s := range list {
		key := strings.Join(s.Tags, ",") + "|" + strings.Join(s.Paths, ",")
		if _, seen := newest[key]; !seen {
			order = append(order, key)
		}
		if old, ok := newest[key]; !ok || s.Time.After(old.Time) {
			newest[key] = s
		}
	}
	plan := &PullPlan{}
	for _, k := range order {
		plan.Snapshots = append(plan.Snapshots, newest[k])
	}
	return plan, nil
}

// Pull restores every folder of a plan into into/<folder name>.
func (rn *Runner) Pull(ctx context.Context, sourcePC, sourceKey string, plan *PullPlan, into string, progress func(done, total int, folder string)) error {
	r, _ := rn.sourceRestic(proto.RestoreJob{SourcePC: sourcePC, SourceKey: sourceKey})
	errs := &tail{}
	r.Stdout, r.Stderr = nil, errs
	total := 0
	for _, s := range plan.Snapshots {
		total += len(s.Paths)
	}
	done := 0
	for _, s := range plan.Snapshots {
		for _, p := range s.Paths {
			name := filepath.Base(strings.ReplaceAll(p, `\`, "/"))
			dest := uniquePath(filepath.Join(into, name))
			if progress != nil {
				progress(done, total, dest)
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			if err := r.Restore(ctx, s.ID+":"+resticread.TreePath(p), dest, nil, true, nil); err != nil {
				msgs := restic.Messages(errs.String())
				if len(msgs) > 0 {
					return errors.New(strings.Join(lastN(msgs, 5), "\n"))
				}
				return err
			}
			done++
		}
	}
	if progress != nil {
		progress(done, total, "")
	}
	return nil
}

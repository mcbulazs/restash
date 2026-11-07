// Package restic runs the restic binary against a PC's repo on the server.
package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo says where a repo lives and how to open it.
type Repo struct {
	URL      string // e.g. https://restash.bulazs.com/repo/anna/
	Username string // the PC id (restash checks it)
	Password string // the PC token
	Key      string // restic repo password; never leaves the PC unlocked
}

// Restic runs commands against one repo.
type Restic struct {
	Bin    string
	Repo   Repo
	Stdout io.Writer
	Stderr io.Writer
}

// Find returns the restic program to run: $RESTASH_RESTIC if set, else the
// copy built into the agent (unpacked on first use), else one next to this
// program or on the PATH.
func Find() (string, error) {
	if p := os.Getenv("RESTASH_RESTIC"); p != "" {
		return p, nil
	}
	if len(bundledGz) > 0 {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return unpack(filepath.Join(dir, "restash"), bundledGz, strings.TrimSpace(bundledHash), BundledVersion())
	}
	name := "restic"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", errors.New("restic not found: install it or set RESTASH_RESTIC")
	}
	return p, nil
}

// BundledVersion is the version of the restic built into the agent, or ""
// if there is none.
func BundledVersion() string { return strings.TrimSpace(bundledVersion) }

// env builds restic's environment. RESTIC_* variables from the user's shell
// are dropped so they can't point restic at another repo.
func (r *Restic) env() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RESTIC_") {
			out = append(out, kv)
		}
	}
	return append(out,
		"RESTIC_REPOSITORY=rest:"+r.Repo.URL,
		"RESTIC_REST_USERNAME="+r.Repo.Username,
		"RESTIC_REST_PASSWORD="+r.Repo.Password,
		"RESTIC_PASSWORD="+r.Repo.Key,
	)
}

func (r *Restic) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, r.Bin, args...)
	c.Env = r.env()
	c.Stdout = r.Stdout
	c.Stderr = r.Stderr
	return c
}

// Run runs restic with args, passing its output through.
func (r *Restic) Run(ctx context.Context, args ...string) error {
	return r.cmd(ctx, args...).Run()
}

// ErrWrongKey means the repo exists but the key doesn't open it.
var ErrWrongKey = errors.New("the key doesn't open this repo")

// Exists reports whether the repo is set up. If it is but the key doesn't
// open it, it returns true and ErrWrongKey.
func (r *Restic) Exists(ctx context.Context) (bool, error) {
	var stderr bytes.Buffer
	c := r.cmd(ctx, "cat", "config")
	c.Stdout = io.Discard
	c.Stderr = &stderr
	err := c.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 10: // repository does not exist
		return false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 12: // wrong password
		return true, ErrWrongKey
	case strings.Contains(stderr.String(), "(401)"):
		return false, errors.New("the server refused the username or password")
	default:
		return false, fmt.Errorf("opening repo: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
}

// Init creates the repo.
func (r *Restic) Init(ctx context.Context) error {
	return r.Run(ctx, "init")
}

// Summary is the last message of `restic backup --json`.
type Summary struct {
	FilesNew            int     `json:"files_new"`
	FilesChanged        int     `json:"files_changed"`
	FilesUnmodified     int     `json:"files_unmodified"`
	DataAdded           int64   `json:"data_added"`
	DataAddedPacked     int64   `json:"data_added_packed"`
	TotalFilesProcessed int     `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	TotalDuration       float64 `json:"total_duration"`
	SnapshotID          string  `json:"snapshot_id"`
}

// BackupOptions are the rules for one backup run.
type BackupOptions struct {
	Paths    []string
	Tags     []string
	Excludes []string
	// Progress, if set, gets restic's status about every 2 seconds.
	Progress func(BackupProgress)
}

// BackupProgress is how far a backup is. Totals grow while restic is still
// scanning; SecondsRemaining is 0 until it can tell.
type BackupProgress struct {
	Percent          float64 `json:"percent_done"`
	TotalFiles       int64   `json:"total_files"`
	FilesDone        int64   `json:"files_done"`
	TotalBytes       int64   `json:"total_bytes"`
	BytesDone        int64   `json:"bytes_done"`
	SecondsRemaining int64   `json:"seconds_remaining"`
}

// Backup backs up the paths and returns restic's summary. Pack size is left
// at restic's default (16 MiB), well under Cloudflare's 100 MB upload limit.
func (r *Restic) Backup(ctx context.Context, opt BackupOptions) (*Summary, error) {
	if len(opt.Paths) == 0 {
		return nil, errors.New("nothing to back up")
	}
	args := []string{"backup", "--json"}
	for _, t := range opt.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range opt.Excludes {
		args = append(args, "--exclude", e)
	}
	args = append(args, "--")
	args = append(args, opt.Paths...)

	c := r.cmd(ctx, args...)
	if opt.Progress != nil {
		// Without a terminal restic reports only once a minute.
		c.Env = append(c.Env, "RESTIC_PROGRESS_FPS=0.5")
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	errOut := r.Stderr
	if errOut == nil {
		errOut = os.Stderr
	}
	sum, perr := parseBackupOutput(out, errOut, opt.Progress)
	werr := c.Wait()
	var exit *exec.ExitError
	switch {
	case errors.As(werr, &exit) && exit.ExitCode() == 10:
		return nil, ErrNoRepo
	// Exit code 3: snapshot made, but some files couldn't be read.
	case werr != nil && !(errors.As(werr, &exit) && exit.ExitCode() == 3):
		return sum, fmt.Errorf("restic backup: %w", werr)
	case perr != nil:
		return nil, perr
	case sum == nil:
		return nil, errors.New("restic backup gave no summary")
	case werr != nil:
		return sum, ErrIncomplete
	}
	return sum, nil
}

// ErrNoRepo means the repo isn't on the server (restic's exit code 10).
var ErrNoRepo = errors.New("the repo doesn't exist")

// ErrIncomplete means the snapshot was made but some files couldn't be read.
var ErrIncomplete = errors.New("some files could not be read")

// parseBackupOutput reads restic's JSON lines and keeps the summary. Status
// lines go to progress (if set); files restic couldn't read are written to
// errOut as "path: reason" lines.
func parseBackupOutput(rd io.Reader, errOut io.Writer, progress func(BackupProgress)) (*Summary, error) {
	var sum *Summary
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg struct {
			MessageType string `json:"message_type"`
			Item        string `json:"item"`
			Error       struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch msg.MessageType {
		case "status":
			if progress != nil {
				var p BackupProgress
				if json.Unmarshal(sc.Bytes(), &p) == nil {
					progress(p)
				}
			}
		case "summary":
			sum = &Summary{}
			if err := json.Unmarshal(sc.Bytes(), sum); err != nil {
				return nil, err
			}
		case "error":
			if msg.Item != "" {
				fmt.Fprintf(errOut, "%s: %s\n", msg.Item, msg.Error.Message)
			} else {
				fmt.Fprintln(errOut, msg.Error.Message)
			}
		}
	}
	return sum, sc.Err()
}

// Messages turns restic's stderr into readable lines: JSON messages become
// their text, empty lines are dropped.
func Messages(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			Message string `json:"message"`
		}
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &msg) == nil && msg.Message != "" {
			line = msg.Message
		}
		out = append(out, line)
	}
	return out
}

// Snapshot is one entry of `restic snapshots --json`.
type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
}

// Snapshots lists the repo's snapshots, oldest first. With noLock, the
// repo isn't locked (for reading someone else's repo read-only).
func (r *Restic) Snapshots(ctx context.Context, noLock bool) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	if noLock {
		args = append([]string{"--no-lock"}, args...)
	}
	var out, errs bytes.Buffer
	c := r.cmd(ctx, args...)
	c.Stdout, c.Stderr = &out, &errs
	if err := c.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 10 {
			return nil, ErrNoRepo
		}
		return nil, fmt.Errorf("listing backups: %w: %s", err, strings.Join(Messages(errs.String()), " "))
	}
	var list []Snapshot
	return list, json.Unmarshal(out.Bytes(), &list)
}

// RestoreProgress is how far a restore is.
type RestoreProgress struct {
	Percent     float64 `json:"percent_done"`
	TotalFiles  int64   `json:"total_files"`
	FilesDone   int64   `json:"files_restored"`
	TotalBytes  int64   `json:"total_bytes"`
	BytesDone   int64   `json:"bytes_restored"`
	MessageType string  `json:"message_type"`
}

// Restore restores from a snapshot ("id" or "id:/folder") into target,
// limited to the include patterns if any. Errors restic reports go to
// r.Stderr. With noLock, the repo isn't locked.
func (r *Restic) Restore(ctx context.Context, snapshot, target string, includes []string, noLock bool, progress func(RestoreProgress)) error {
	args := []string{"restore", snapshot, "--target", target, "--json"}
	if noLock {
		args = append([]string{"--no-lock"}, args...)
	}
	for _, i := range includes {
		args = append(args, "--include", i)
	}
	c := r.cmd(ctx, args...)
	out, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	errOut := r.Stderr
	if errOut == nil {
		errOut = os.Stderr
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var p RestoreProgress
		if json.Unmarshal(sc.Bytes(), &p) != nil {
			continue
		}
		switch p.MessageType {
		case "status", "summary":
			if p.MessageType == "summary" {
				p.Percent = 1
			}
			if progress != nil {
				progress(p)
			}
		case "error":
			var e struct {
				Item  string `json:"item"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal(sc.Bytes(), &e)
			fmt.Fprintf(errOut, "%s: %s\n", e.Item, e.Error.Message)
		}
	}
	if err := c.Wait(); err != nil {
		return fmt.Errorf("restic restore: %w", err)
	}
	return nil
}

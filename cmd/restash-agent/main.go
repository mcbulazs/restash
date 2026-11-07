// restash-agent backs up folders on this PC to a restash server.
//
// For now it is run by hand:
//
//	restash-agent init -server https://restash.bulazs.com -user anna -password ...
//	restash-agent backup ~/Documents
//	restash-agent snapshots
//	restash-agent restore -target ~/restored latest
//	restash-agent forget -tag entry:manual -keep-daily 7
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/agent"
	"github.com/mcbulazs/restash/internal/restic"
)

const usage = `usage: restash-agent <command> [flags]

commands:
  init       connect this PC to a repo on the server (creates it if new)
  backup     back up folders
  snapshots  list backups
  restore    restore a backup into a folder
  forget     remove old backups by retention rules

Run "restash-agent <command> -h" for a command's flags.
`

type command func(ctx context.Context, cfgPath string, args []string) error

var commands = map[string]command{
	"init":      cmdInit,
	"backup":    cmdBackup,
	"snapshots": cmdSnapshots,
	"restore":   cmdRestore,
	"forget":    cmdForget,
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cfgPath := os.Getenv("RESTASH_CONFIG")
	if cfgPath == "" {
		p, err := agent.DefaultPath()
		if err != nil {
			fail(err)
		}
		cfgPath = p
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cmd(ctx, cfgPath, os.Args[2:]); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "restash-agent:", err)
	os.Exit(1)
}

func newRestic(cfg *agent.Config) (*restic.Restic, error) {
	bin, err := restic.Find()
	if err != nil {
		return nil, err
	}
	return &restic.Restic{Bin: bin, Repo: cfg.Repo(), Stdout: os.Stdout, Stderr: os.Stderr}, nil
}

func load(cfgPath string) (*agent.Config, *restic.Restic, error) {
	cfg, err := agent.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	r, err := newRestic(cfg)
	return cfg, r, err
}

func cmdInit(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	server := fs.String("server", "https://restash.bulazs.com", "restash server URL")
	user := fs.String("user", "", "storage username")
	password := fs.String("password", os.Getenv("RESTASH_PASSWORD"), "storage password (or $RESTASH_PASSWORD)")
	repo := fs.String("repo", "", "repo path on the server (default: the username)")
	key := fs.String("key", os.Getenv("RESTASH_KEY"), "key of an existing repo, to connect to it (or $RESTASH_KEY)")
	force := fs.Bool("force", false, "replace an existing config on this PC")
	fs.Parse(args)

	if _, err := os.Stat(cfgPath); err == nil && !*force {
		return fmt.Errorf("this PC is already set up (%s); use -force to replace it", cfgPath)
	}
	cfg := &agent.Config{
		Server:   strings.TrimSuffix(*server, "/"),
		RepoPath: strings.Trim(*repo, "/"),
		Username: *user,
		Password: *password,
		Key:      *key,
	}
	if cfg.RepoPath == "" {
		cfg.RepoPath = cfg.Username
	}
	newKey := cfg.Key == ""
	if newKey {
		cfg.Key = agent.NewKey()
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	r, err := newRestic(cfg)
	if err != nil {
		return err
	}

	exists, err := r.Exists(ctx)
	switch {
	case errors.Is(err, restic.ErrWrongKey) && newKey:
		return errors.New("a repo already exists at that path: pass its -key to connect to it")
	case err != nil:
		return err
	}
	switch {
	case exists && newKey:
		return errors.New("a repo already exists at that path: pass its -key to connect to it")
	case !exists && !newKey:
		return errors.New("no repo at that path to connect to")
	case !exists:
		if err := r.Init(ctx); err != nil {
			return fmt.Errorf("creating repo: %w", err)
		}
	}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	fmt.Printf("\nThis PC is set up. Settings saved to %s\n", cfgPath)
	if newKey {
		fmt.Printf("\nRepo key (keep it somewhere safe, you need it to restore on another PC):\n\n  %s\n\n", cfg.Key)
	}
	return nil
}

// list collects repeated flag values, e.g. -exclude a -exclude b.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func cmdBackup(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	var tags, excludes list
	fs.Var(&tags, "tag", "tag for the snapshot, repeatable (default entry:manual)")
	fs.Var(&excludes, "exclude", "pattern to skip, repeatable (e.g. node_modules)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("usage: restash-agent backup [-tag t] [-exclude p] <folder>...")
	}
	if len(tags) == 0 {
		tags = list{"entry:manual"}
	}
	_, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	r.Stdout = nil // JSON progress is parsed, not shown
	sum, err := r.Backup(ctx, restic.BackupOptions{Paths: fs.Args(), Tags: tags, Excludes: excludes})
	if sum != nil {
		fmt.Printf("Backed up %d files (%s) in %s: %d new, %d changed, %s uploaded. Snapshot %s.\n",
			sum.TotalFilesProcessed, size(sum.TotalBytesProcessed),
			(time.Duration(sum.TotalDuration * float64(time.Second))).Round(time.Second),
			sum.FilesNew, sum.FilesChanged, size(sum.DataAddedPacked), shortID(sum.SnapshotID))
	}
	if errors.Is(err, restic.ErrIncomplete) {
		fmt.Fprintln(os.Stderr, "Warning: some files could not be read and were skipped.")
		return nil
	}
	return err
}

func cmdSnapshots(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("snapshots", flag.ExitOnError)
	tag := fs.String("tag", "", "only show snapshots with this tag")
	fs.Parse(args)
	_, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	a := []string{"snapshots"}
	if *tag != "" {
		a = append(a, "--tag", *tag)
	}
	return r.Run(ctx, a...)
}

func cmdRestore(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	target := fs.String("target", "", "folder to restore into (required)")
	var includes list
	fs.Var(&includes, "include", "only restore this path, repeatable")
	fs.Parse(args)
	if *target == "" || fs.NArg() != 1 {
		return errors.New("usage: restash-agent restore -target <folder> [-include path] <snapshot|latest>")
	}
	_, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	a := []string{"restore", fs.Arg(0), "--target", *target}
	for _, i := range includes {
		a = append(a, "--include", i)
	}
	return r.Run(ctx, a...)
}

func cmdForget(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("forget", flag.ExitOnError)
	tag := fs.String("tag", "", "entry tag to apply the rules to, e.g. entry:manual (required)")
	keep := map[string]*int{}
	for _, k := range []string{"last", "hourly", "daily", "weekly", "monthly", "yearly"} {
		keep[k] = fs.Int("keep-"+k, 0, "keep the last N "+k+" snapshots")
	}
	dryRun := fs.Bool("dry-run", false, "only show what would be removed")
	fs.Parse(args)
	if *tag == "" {
		return errors.New("-tag is required, so one entry's rules never remove another's backups")
	}
	a := []string{"forget", "--tag", *tag, "--group-by", "tags"}
	hasRule := false
	for _, k := range []string{"last", "hourly", "daily", "weekly", "monthly", "yearly"} {
		if n := *keep[k]; n > 0 {
			a = append(a, fmt.Sprintf("--keep-%s=%d", k, n))
			hasRule = true
		}
	}
	if !hasRule {
		return errors.New("give at least one -keep-* rule")
	}
	if *dryRun {
		a = append(a, "--dry-run")
	} else {
		a = append(a, "--prune")
	}
	_, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	return r.Run(ctx, a...)
}

func size(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

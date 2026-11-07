// restash-agent backs up folders on this PC to a restash server.
//
//	restash-agent join K7Q2MX          link this PC (code from "Add a PC")
//	restash-agent run                  stay linked to the server
//	restash-agent backup ~/Documents   back up folders
//	restash-agent snapshots            list backups
//	restash-agent restore -target ~/restored latest
//	restash-agent forget -tag entry:manual -keep-daily 7
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/mcbulazs/restash/internal/agent"
	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/restic"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: restash-agent <command> [flags]

commands:
  join       link this PC to your account with the code from "Add a PC"
  unlock     type your password again to unlock this PC's backups
  run        stay linked to the server (shows the PC as online)
  backup     back up folders
  snapshots  list backups
  restore    restore a backup into a folder
  forget     remove old backups by retention rules
  version    show the version

Run "restash-agent <command> -h" for a command's flags.
`

type command func(ctx context.Context, cfgPath string, args []string) error

var commands = map[string]command{
	"join":      cmdJoin,
	"unlock":    cmdUnlock,
	"run":       cmdRun,
	"backup":    cmdBackup,
	"snapshots": cmdSnapshots,
	"restore":   cmdRestore,
	"forget":    cmdForget,
	"version": func(context.Context, string, []string) error {
		fmt.Println("restash-agent", version, runtime.GOOS+"/"+runtime.GOARCH)
		return nil
	},
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

// load reads the config and checks the PC is unlocked.
func load(cfgPath string) (*agent.Config, *restic.Restic, error) {
	cfg, err := agent.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if cfg.RepoKey == "" {
		return nil, nil, errors.New("this PC is linked but locked: run `restash-agent unlock`")
	}
	r, err := newRestic(cfg)
	return cfg, r, err
}

// readPassword asks for the account password without echoing it.
// $RESTASH_ACCOUNT_PASSWORD is used instead if set (for scripts).
func readPassword(email string) (string, error) {
	if p := os.Getenv("RESTASH_ACCOUNT_PASSWORD"); p != "" {
		return p, nil
	}
	fmt.Fprintf(os.Stderr, "Password for %s: ", email)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// unlock asks for the password (up to three times) and unlocks this PC.
func unlock(ctx context.Context, cfgPath string, cfg *agent.Config, email string) error {
	r, err := newRestic(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Your password unlocks this PC's backups. It is checked here and never sent anywhere.")
	for try := 1; ; try++ {
		pw, err := readPassword(email)
		if err != nil {
			return err
		}
		err = agent.Unlock(ctx, cfg, pw, r)
		if errors.Is(err, agent.ErrWrongPassword) && try < 3 && os.Getenv("RESTASH_ACCOUNT_PASSWORD") == "" {
			fmt.Fprintln(os.Stderr, "That password didn't work. Try again.")
			continue
		}
		if err != nil {
			return err
		}
		return cfg.Save(cfgPath)
	}
}

func cmdJoin(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	server := fs.String("server", "https://restash.bulazs.com", "restash server address")
	name := fs.String("name", agent.PCName(), "name for this PC")
	force := fs.Bool("force", false, "replace this PC's existing link")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New(`usage: restash-agent join [-server URL] <code from "Add a PC">`)
	}
	*server = strings.TrimSuffix(*server, "/")
	if err := agent.CheckServer(*server); err != nil {
		return err
	}
	if _, err := agent.Load(cfgPath); err == nil && !*force {
		return fmt.Errorf("this PC is already linked (%s); use -force to link it again", cfgPath)
	}
	c := agent.NewClient(*server, "", "")
	res, err := c.Join(ctx, proto.JoinRequest{Code: fs.Arg(0), Name: *name, OS: agent.OSName(), Arch: runtime.GOARCH, Version: version})
	if err != nil {
		return err
	}
	cfg := &agent.Config{Server: *server, PCID: res.PCID, Token: res.Token}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Linked %q to %s's account (%s).\n\n", *name, res.Name, res.Email)
	if err := unlock(ctx, cfgPath, cfg, res.Email); err != nil {
		return fmt.Errorf("%w\nThe PC is linked; run `restash-agent unlock` to try again", err)
	}
	fmt.Fprintln(os.Stderr, "\nAll set. This PC can back up now, and shows up on the website.")
	return nil
}

func cmdUnlock(ctx context.Context, cfgPath string, args []string) error {
	cfg, err := agent.Load(cfgPath)
	if err != nil {
		return err
	}
	acc, err := agent.NewClient(cfg.Server, cfg.PCID, cfg.Token).Account(ctx)
	if err != nil {
		return err
	}
	if err := unlock(ctx, cfgPath, cfg, acc.Email); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Unlocked.")
	return nil
}

func cmdRun(ctx context.Context, cfgPath string, args []string) error {
	cfg, err := agent.Load(cfgPath)
	if err != nil {
		return err
	}
	slog.Info("linking to the server", "server", cfg.Server, "pc", cfg.PCID)
	return agent.RunLink(ctx, cfg, version, func() { slog.Info("linked; this PC shows as online") })
}

// list collects repeated flag values, e.g. -exclude a -exclude b.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func cmdBackup(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	entry := fs.String("entry", "manual", "entry this backup belongs to (tagged entry:<name>)")
	var excludes list
	fs.Var(&excludes, "exclude", "pattern to skip, repeatable (e.g. node_modules)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("usage: restash-agent backup [-entry name] [-exclude p] <folder>...")
	}
	cfg, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	r.Stdout = nil // JSON progress is parsed, not shown
	started := time.Now()
	sum, err := r.Backup(ctx, restic.BackupOptions{Paths: fs.Args(), Tags: []string{"entry:" + *entry}, Excludes: excludes})
	report := proto.BackupReport{Entry: *entry, StartedAt: started, FinishedAt: time.Now(), OK: sum != nil}
	if sum != nil {
		report.Files, report.Bytes, report.Added, report.Snapshot = int64(sum.TotalFilesProcessed), sum.TotalBytesProcessed, sum.DataAddedPacked, sum.SnapshotID
		fmt.Printf("Backed up %d files (%s) in %s: %d new, %d changed, %s uploaded. Snapshot %s.\n",
			sum.TotalFilesProcessed, size(sum.TotalBytesProcessed),
			(time.Duration(sum.TotalDuration * float64(time.Second))).Round(time.Second),
			sum.FilesNew, sum.FilesChanged, size(sum.DataAddedPacked), shortID(sum.SnapshotID))
	}
	rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if rerr := agent.NewClient(cfg.Server, cfg.PCID, cfg.Token).ReportBackup(rctx, report); rerr != nil {
		fmt.Fprintln(os.Stderr, "Warning: couldn't tell the server about this backup:", rerr)
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
	periods := []string{"last", "hourly", "daily", "weekly", "monthly", "yearly"}
	keep := map[string]*int{}
	for _, k := range periods {
		keep[k] = fs.Int("keep-"+k, 0, "keep the last N "+k+" snapshots")
	}
	dryRun := fs.Bool("dry-run", false, "only show what would be removed")
	fs.Parse(args)
	if *tag == "" {
		return errors.New("-tag is required, so one entry's rules never remove another's backups")
	}
	a := []string{"forget", "--tag", *tag, "--group-by", "tags"}
	hasRule := false
	for _, k := range periods {
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

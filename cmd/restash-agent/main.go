// restash-agent backs up folders on this PC to a restash server.
//
// Downloaded from the website as restash-setup-K7Q2MX, it sets itself up
// when opened: links the PC, asks for the password, installs the service.
//
//	restash-agent setup K7Q2MX         link this PC and install the service
//	restash-agent join K7Q2MX          link this PC (code from "Add a PC")
//	restash-agent run                  stay linked, back up on schedule
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
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/mcbulazs/restash/internal/agent"
	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/restic"
	"github.com/mcbulazs/restash/internal/vault"
)

// lastPassword holds the password typed during join for a moment, so the
// offer to bring back another PC's files doesn't ask for it twice.
var lastPassword string

func apiClient(cfg *agent.Config) *agent.Client {
	return agent.NewClient(cfg.Server, cfg.PCID, cfg.Token)
}

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: restash-agent <command> [flags]

commands:
  setup      link this PC and keep it backed up in the background
  status     show whether this PC is linked and the service is running
  join       link this PC to your account with the code from "Add a PC"
  unlock     type your password again to unlock this PC's backups
  run        stay linked and back up entries on their schedules
  entries    list this PC's entries and when they run next
  pull       bring back another of your PCs' files onto this one
  backup     back up folders
  snapshots  list backups
  restore    restore a backup into a folder
  forget     remove old backups by retention rules
  install    run in the background and start with the PC
  uninstall  stop running in the background
  version    show the version

Run "restash-agent <command> -h" for a command's flags.
`

type command func(ctx context.Context, cfgPath string, args []string) error

var commands = map[string]command{
	"setup":     cmdSetup,
	"install":   cmdInstall,
	"uninstall": cmdUninstall,
	"status":    cmdStatus,
	"join":      cmdJoin,
	"unlock":    cmdUnlock,
	"run":       cmdRun,
	"entries":   cmdEntries,
	"pull":      cmdPull,
	"backup":    cmdBackup,
	"snapshots": cmdSnapshots,
	"restore":   cmdRestore,
	"forget":    cmdForget,
	"version": func(context.Context, string, []string) error {
		fmt.Println("restash-agent", version, runtime.GOOS+"/"+runtime.GOARCH)
		if v := restic.BundledVersion(); v != "" {
			fmt.Println("restic", v, "(built in)")
		} else {
			fmt.Println("restic not built in; using the one installed on this PC")
		}
		return nil
	},
}

func main() {
	args := os.Args[1:]
	// Opened as a downloaded setup file (double-click): set up.
	if len(args) == 0 && isSetupName(os.Args[0]) {
		args = []string{"setup"}
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		pause()
		os.Exit(2)
	}
	cmd, ok := commands[args[0]]
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
	err := cmd(ctx, cfgPath, args[1:])
	if errors.Is(err, agent.ErrUpdated) {
		fmt.Fprintln(os.Stderr, "restash-agent: updated; the service starts the new version")
		os.Exit(3)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "restash-agent:", err)
	pause()
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
		if err == nil {
			lastPassword = pw
		}
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
	server := fs.String("server", serverDefault(), "restash server address")
	name := fs.String("name", agent.PCName(), "name for this PC")
	force := fs.Bool("force", false, "replace this PC's existing link")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New(`usage: restash-agent join [-server URL] <code from "Add a PC">`)
	}
	home, _ := os.UserHomeDir()
	if err := joinPC(ctx, cfgPath, *server, *name, fs.Arg(0), home, *force); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "\nAll set. Pick folders to back up on the website, on this PC's page.")
	fmt.Fprintln(os.Stderr, "Run `restash-agent install` so backups happen in the background.")
	return nil
}

// joinPC links this PC with a code, unlocks it with the password, and
// offers to bring back another PC's files.
func joinPC(ctx context.Context, cfgPath, server, name, code, home string, force bool) error {
	server = strings.TrimSuffix(server, "/")
	if err := agent.CheckServer(server); err != nil {
		return err
	}
	if _, err := agent.Load(cfgPath); err == nil && !force {
		return fmt.Errorf("this PC is already linked (%s); use -force to link it again", cfgPath)
	}
	c := agent.NewClient(server, "", "")
	res, err := c.Join(ctx, proto.JoinRequest{Code: strings.ToUpper(code), Name: name, OS: agent.OSName(), Arch: runtime.GOARCH, Version: version})
	if err != nil {
		return err
	}
	cfg := &agent.Config{Server: server, PCID: res.PCID, Token: res.Token}
	if runtime.GOOS == "windows" {
		cfg.Home = home // the service runs as SYSTEM, whose home isn't the person's
	}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Linked %q to %s's account (%s).\n\n", name, res.Name, res.Email)
	if err := unlock(ctx, cfgPath, cfg, res.Email); err != nil {
		return fmt.Errorf("%w\nThe PC is linked; run `restash-agent unlock` to try again", err)
	}
	// A new PC after another one died: offer to bring its files back.
	if others, err := apiClient(cfg).OtherPCs(ctx); err == nil && len(others) > 0 && term.IsTerminal(int(os.Stdin.Fd())) {
		if pw := lastPassword; pw != "" {
			if accountKey, _, err := agent.AccountKey(ctx, apiClient(cfg), pw); err == nil {
				fmt.Fprintln(os.Stderr, "\nThis PC is new to restash, but your account has backups of other PCs.")
				r, rerr := newRestic(cfg)
				if rerr == nil {
					if err := pullFrom(ctx, cfgPath, cfg, r, others, accountKey, "", "", cfg.HomeDir()); err != nil {
						fmt.Fprintln(os.Stderr, "Couldn't bring the files back:", err)
					}
				}
			}
		}
	}
	lastPassword = ""
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
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	asService := fs.Bool("service", false, "running as the background service (also keeps the agent updated)")
	fs.Parse(args)
	if handled, err := runAsService(func(ctx context.Context) error { return runAgent(ctx, cfgPath, *asService) }); handled {
		return err
	}
	return runAgent(ctx, cfgPath, *asService)
}

func runAgent(ctx context.Context, cfgPath string, asService bool) error {
	cfg, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	rn, err := agent.NewRunner(cfg, agent.StatePath(cfgPath), r)
	if err != nil {
		return err
	}
	pcKey, err := vault.PCKeyFromPassword(cfg.RepoKey)
	if err != nil {
		return err
	}
	entryKey := vault.EntryKey(pcKey)

	syncEntries := func() {
		sctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := rn.Sync(sctx); err != nil {
			slog.Warn("couldn't fetch entries", "err", err)
		}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if asService && os.Getenv("RESTASH_NO_UPDATE") == "" {
		if exe, err := os.Executable(); err == nil {
			u := &agent.Updater{Current: version, Exe: exe, API: envOr("RESTASH_UPDATE_API", agent.ReleasesAPI), Download: envOr("RESTASH_UPDATE_DOWNLOAD", agent.ReleasesDownload)}
			go func() {
				if err := u.KeepUpdated(ctx, rn.Busy, slog.Info); errors.Is(err, agent.ErrUpdated) {
					cancel(err)
				}
			}()
		}
	}
	go rn.Run(ctx)
	slog.Info("linking to the server", "server", cfg.Server, "pc", cfg.PCID, "version", version)
	err = agent.RunLink(ctx, cfg, version, agent.LinkHandler{
		OnUp: func() {
			slog.Info("linked; this PC shows as online")
			go syncEntries() // pick up changes made while offline
		},
		OnEntries: func() { go syncEntries() },
		OnRun: func(entry string) {
			slog.Info("asked to back up now", "entry", entry)
			go rn.RunNow(entry)
		},
		OnBrowse: func(data string) (string, error) { return agent.AnswerBrowse(entryKey, cfg.PCID, cfg.HomeDir(), data) },
		OnRestore: func(id, data string, send func(proto.Message)) {
			slog.Info("restoring files", "job", id)
			rn.HandleRestore(ctx, id, data, send)
			slog.Info("restore finished", "job", id)
		},
	})
	if cause := context.Cause(ctx); errors.Is(cause, agent.ErrUpdated) {
		return cause
	}
	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func cmdPull(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	into := fs.String("into", "", "folder to put the files in (default: <home>/Restored from <PC>)")
	from := fs.String("from", "", "name of the PC to bring back (asked if not given)")
	fs.Parse(args)
	cfg, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	c := agent.NewClient(cfg.Server, cfg.PCID, cfg.Token)
	others, err := c.OtherPCs(ctx)
	if err != nil {
		return err
	}
	if len(others) == 0 {
		return errors.New("there are no other PCs with backups in your account")
	}
	acc, err := c.Account(ctx)
	if err != nil {
		return err
	}
	var accountKey []byte
	for try := 1; ; try++ {
		pw, err := readPassword(acc.Email)
		if err != nil {
			return err
		}
		accountKey, _, err = agent.AccountKey(ctx, c, pw)
		if errors.Is(err, agent.ErrWrongPassword) && try < 3 && os.Getenv("RESTASH_ACCOUNT_PASSWORD") == "" {
			fmt.Fprintln(os.Stderr, "That password didn't work. Try again.")
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	return pullFrom(ctx, cfgPath, cfg, r, others, accountKey, *from, *into, cfg.HomeDir())
}

// pullFrom asks which PC to bring back (unless from names one), then
// restores the newest backup of each of its entries.
func pullFrom(ctx context.Context, cfgPath string, cfg *agent.Config, r *restic.Restic, others []proto.OtherPC, accountKey []byte, from, into, home string) error {
	var pick *proto.OtherPC
	for i := range others {
		if strings.EqualFold(others[i].Name, from) {
			pick = &others[i]
		}
	}
	if pick == nil {
		if from != "" {
			return fmt.Errorf("no PC called %q in your account", from)
		}
		fmt.Fprintln(os.Stderr, "\nWhich PC's files do you want to bring back?")
		for i, o := range others {
			last := "no backups yet"
			if o.LastBackup != nil {
				last = "last backup " + o.LastBackup.Local().Format("Jan 2 15:04")
			}
			fmt.Fprintf(os.Stderr, "  %d. %s (%s, %s, %s)\n", i+1, o.Name, o.OS, last, size(o.Stored))
		}
		fmt.Fprint(os.Stderr, "Number, or Enter to skip: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Fprintln(os.Stderr, "Skipped. You can do this later with `restash-agent pull`.")
			return nil
		}
		n := 0
		fmt.Sscan(line, &n)
		if n < 1 || n > len(others) {
			return errors.New("no such number")
		}
		pick = &others[n-1]
	}
	key, err := agent.OtherPCKey(accountKey, *pick)
	if err != nil {
		return fmt.Errorf("can't unlock %s's backup: %w", pick.Name, err)
	}
	rn, err := agent.NewRunner(cfg, agent.StatePath(cfgPath), r)
	if err != nil {
		return err
	}
	plan, err := rn.PlanPull(ctx, pick.ID, key)
	if err != nil {
		return err
	}
	if len(plan.Snapshots) == 0 {
		return fmt.Errorf("%s has no backups yet", pick.Name)
	}
	if into == "" {
		into = filepath.Join(home, "Restored from "+pick.Name)
	}
	fmt.Fprintf(os.Stderr, "\nBringing back %s's newest backups into %s\n", pick.Name, into)
	err = rn.Pull(ctx, pick.ID, key, plan, into, func(done, total int, folder string) {
		if folder != "" {
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n", done+1, total, folder)
		}
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Done. Everything is in %s\n", into)
	return nil
}

func cmdEntries(ctx context.Context, cfgPath string, args []string) error {
	cfg, r, err := load(cfgPath)
	if err != nil {
		return err
	}
	rn, err := agent.NewRunner(cfg, agent.StatePath(cfgPath), r)
	if err != nil {
		return err
	}
	if err := rn.Sync(ctx); err != nil {
		return err
	}
	list := rn.Entries()
	if len(list) == 0 {
		fmt.Println("No entries yet. Add one on the website, on this PC's page.")
		return nil
	}
	now := time.Now()
	for _, e := range list {
		last := rn.LastRun(e.ID)
		next := "only when asked"
		if t, ok := e.Schedule.Next(last, now); ok {
			next = "now"
			if t.After(now) {
				next = t.Format("Mon Jan 2 15:04")
			}
		}
		lastS := "never"
		if !last.IsZero() {
			lastS = last.Format("Mon Jan 2 15:04")
		}
		fmt.Printf("%s (%s)\n  folders: %s\n", e.Name, e.ID, strings.Join(e.Paths, ", "))
		if len(e.Skips) > 0 {
			fmt.Printf("  skips:   %s\n", strings.Join(e.Skips, ", "))
		}
		fmt.Printf("  last run: %s, next: %s\n", lastS, next)
	}
	return nil
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

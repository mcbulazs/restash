package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/agent"
	"github.com/mcbulazs/restash/internal/service"
)

// defaultServer can be changed at build time with
// -ldflags "-X main.defaultServer=https://...". $RESTASH_SERVER wins.
var defaultServer = "https://restash.bulazs.com"

func serverDefault() string {
	if s := os.Getenv("RESTASH_SERVER"); s != "" {
		return s
	}
	return defaultServer
}

// The website names downloads restash-setup-K7Q2MX(.exe). Browsers may add
// " (1)" or similar, so this only looks for the code after the prefix.
var setupName = regexp.MustCompile(`(?i)^restash-setup[-_ ]*([23456789a-hj-np-z]{6})(?:[^0-9a-z]|$)`)

// codeFromName finds the join code in the binary's file name.
func codeFromName(path string) string {
	m := setupName.FindStringSubmatch(baseName(path))
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// isSetupName says whether the binary was downloaded as a setup file.
func isSetupName(path string) bool {
	return strings.HasPrefix(strings.ToLower(baseName(path)), "restash-setup")
}

// baseName is the file name after the last / or \ on any system.
func baseName(path string) string {
	return path[strings.LastIndexAny(path, `/\`)+1:]
}

func cmdSetup(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	server := fs.String("server", serverDefault(), "restash server address")
	code := fs.String("code", "", `code from "Add a PC" (read from the file name if not given)`)
	name := fs.String("name", agent.PCName(), "name for this PC")
	home, _ := os.UserHomeDir()
	fs.StringVar(&home, "home", home, "your home folder (for restores)")
	fs.Parse(args)
	if *code == "" && fs.NArg() == 1 {
		*code = fs.Arg(0)
	}
	if *code == "" {
		*code = codeFromName(os.Args[0])
	}

	fmt.Fprintln(os.Stderr, "restash setup")
	fmt.Fprintln(os.Stderr, "Links this PC to your restash account and keeps it backed up in the background.")
	fmt.Fprintln(os.Stderr)

	if !elevated() {
		// Windows: installing a service needs an administrator. Ask once.
		fmt.Fprintln(os.Stderr, "Windows will ask whether restash may make changes. Click Yes, and setup continues in a new window.")
		err := relaunchElevated([]string{"setup", "-server", *server, "-code", *code, "-name", *name, "-home", home})
		if err != nil {
			return fmt.Errorf("couldn't get administrator rights: %w", err)
		}
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}

	cfg, err := agent.Load(cfgPath)
	switch {
	case err == nil && cfg.RepoKey != "":
		fmt.Fprintf(os.Stderr, "This PC is already linked to %s. Updating the background service.\n", cfg.Server)
	case err == nil:
		acc, err := apiClient(cfg).Account(ctx)
		if err != nil {
			return err
		}
		if err := unlock(ctx, cfgPath, cfg, acc.Email); err != nil {
			return err
		}
	default:
		if *code == "" {
			fmt.Fprint(os.Stderr, `Type the code from the website ("Add a PC"): `)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			*code = strings.ToUpper(strings.TrimSpace(line))
			if *code == "" {
				return errors.New("no code given")
			}
		}
		if err := joinPC(ctx, cfgPath, *server, *name, *code, home, false); err != nil {
			return err
		}
	}

	fmt.Fprintln(os.Stderr, "\nSetting up the background service…")
	dst, err := service.Install(keepNewer)
	if err != nil {
		return fmt.Errorf("%w\nThe PC is linked; you can run `restash-agent run` yourself instead", err)
	}
	fmt.Fprintln(os.Stderr, "\nAll set. restash runs in the background and starts with this PC.")
	fmt.Fprintln(os.Stderr, "Pick folders to back up on the website, on this PC's page.")
	afterInstall(dst)
	pause()
	return nil
}

// afterInstall says where things are, and what macOS needs.
func afterInstall(dst string) {
	fmt.Fprintf(os.Stderr, "\nThe agent is at %s\nIts log: %s\n", dst, service.LogHint())
	if runtime.GOOS == "darwin" {
		fmt.Fprintln(os.Stderr, "\nmacOS keeps some folders (Desktop, Documents, Mail…) private. To back them up, open")
		fmt.Fprintln(os.Stderr, "System Settings → Privacy & Security → Full Disk Access, click +, press ⌘⇧G and paste:")
		fmt.Fprintf(os.Stderr, "  %s\n", dst)
	}
	if runtime.GOOS == "linux" {
		fmt.Fprintln(os.Stderr, "\nYou can delete the file you downloaded.")
	}
}

func cmdInstall(ctx context.Context, cfgPath string, args []string) error {
	if !elevated() {
		return errors.New("installing the service needs an administrator: run this from a terminal opened with \"Run as administrator\"")
	}
	if _, err := agent.Load(cfgPath); err != nil {
		return err
	}
	dst, err := service.Install(keepNewer)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "restash runs in the background now and starts with this PC.")
	afterInstall(dst)
	return nil
}

func cmdUninstall(ctx context.Context, cfgPath string, args []string) error {
	if !elevated() {
		return errors.New("removing the service needs an administrator: run this from a terminal opened with \"Run as administrator\"")
	}
	if err := service.Uninstall(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "The background service is gone; backups stop.")
	fmt.Fprintf(os.Stderr, "This PC's link stays in %s. Remove the PC on the website to unlink it.\n", filepath.Dir(cfgPath))
	return nil
}

func cmdStatus(ctx context.Context, cfgPath string, args []string) error {
	fmt.Println("restash-agent", version)
	cfg, err := agent.Load(cfgPath)
	switch {
	case errors.Is(err, agent.ErrNotJoined):
		fmt.Println("Not linked to an account yet.")
	case err != nil:
		return err
	case cfg.RepoKey == "":
		fmt.Printf("Linked to %s, but locked: run `restash-agent unlock`.\n", cfg.Server)
	default:
		fmt.Printf("Linked to %s and unlocked.\n", cfg.Server)
	}
	if service.Running() {
		fmt.Println("The background service is running. Log:", service.LogHint())
	} else {
		fmt.Println("The background service isn't running: run `restash-agent install`.")
	}
	return nil
}

// keepNewer says whether the agent already installed at path is newer than
// this one (it updates itself), so setup doesn't put an older one back.
func keepNewer(path string) bool {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return false
	}
	f := strings.Fields(string(out))
	if len(f) < 2 || !agent.Newer(f[1], version) {
		return false
	}
	fmt.Fprintf(os.Stderr, "Keeping the newer restash-agent %s that's already installed.\n", f[1])
	return true
}

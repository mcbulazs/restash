package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// BinaryPath is ~/.local/share/restash/restash-agent: the agent runs as
// the person, so it can update itself there.
func BinaryPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "restash", Name), nil
}

func unitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "systemd", "user", Name+".service"), nil
}

// Unit is the systemd unit for the agent at exe.
func Unit(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=restash backups
Documentation=https://github.com/mcbulazs/restash
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s %s
Restart=always
RestartSec=10
# Backups shouldn't make the PC feel slow.
Nice=10
IOSchedulingClass=idle

[Install]
WantedBy=default.target
`, quote(exe), strings.Join(Args, " "))
}

// quote quotes a path for ExecStart if it has spaces.
func quote(s string) string {
	if strings.ContainsAny(s, " \t\"\\") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func stopIfRunning() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}
	exec.Command("systemctl", "--user", "stop", Name).Run()
	return nil
}

func install(exe string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("this Linux has no systemd; keep `%s run` going another way (e.g. your desktop's autostart)", exe)
	}
	p, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(Unit(exe)), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", Name); err != nil {
		return err
	}
	if err := systemctl("restart", Name); err != nil {
		return err
	}
	// Keep running after logging out and start at boot, not just at login.
	if u, err := user.Current(); err == nil {
		exec.Command("loginctl", "enable-linger", u.Username).Run()
	}
	linkCommand(exe)
	return nil
}

// linkCommand puts restash-agent on the PATH (~/.local/bin) if that
// folder is in use.
func linkCommand(exe string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	bin := filepath.Join(home, ".local", "bin")
	if !strings.Contains(os.Getenv("PATH"), bin) {
		return
	}
	link := filepath.Join(bin, Name)
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return // something else is there
	}
	os.MkdirAll(bin, 0o755)
	os.Remove(link)
	os.Symlink(exe, link)
}

func uninstall() error {
	if _, err := exec.LookPath("systemctl"); err == nil {
		exec.Command("systemctl", "--user", "disable", "--now", Name).Run()
	}
	p, err := unitPath()
	if err != nil {
		return err
	}
	os.Remove(p)
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	if home, err := os.UserHomeDir(); err == nil {
		link := filepath.Join(home, ".local", "bin", Name)
		if t, err := os.Readlink(link); err == nil && filepath.Base(t) == Name {
			os.Remove(link)
		}
	}
	return nil
}

// Running says whether the service is running.
func Running() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", Name).Run() == nil
}

// LogHint tells where the service's log is.
func LogHint() string { return "journalctl --user -u " + Name }

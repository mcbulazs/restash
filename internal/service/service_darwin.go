package service

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const label = "com.bulazs.restash.agent"

// BinaryPath is ~/Library/Application Support/restash/restash-agent: the
// agent runs as the person, so it can update itself there.
func BinaryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "restash", Name), nil
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func logPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "restash-agent.log")
}

// Plist is the launchd agent for the agent at exe.
func Plist(exe string) string {
	var args strings.Builder
	for _, a := range append([]string{exe}, Args...) {
		fmt.Fprintf(&args, "\n    <string>%s</string>", html.EscapeString(a))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>%s
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>LowPriorityBackgroundIO</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, label, args.String(), html.EscapeString(logPath()), html.EscapeString(logPath()))
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func stopIfRunning() error {
	exec.Command("launchctl", "bootout", domain()+"/"+label).Run()
	return nil
}

func install(exe string) error {
	p, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(logPath()), 0o755)
	if err := os.WriteFile(p, []byte(Plist(exe)), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain(), p).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstall() error {
	stopIfRunning()
	if p, err := plistPath(); err == nil {
		os.Remove(p)
	}
	return nil
}

// Running says whether the service is running.
func Running() bool {
	return exec.Command("launchctl", "print", domain()+"/"+label).Run() == nil
}

// LogHint tells where the service's log is.
func LogHint() string { return logPath() }

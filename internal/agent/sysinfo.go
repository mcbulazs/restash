package agent

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PCName is the name the PC shows up with on the website.
func PCName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "PC"
	}
	return strings.TrimSuffix(h, ".local")
}

// OSName is a readable OS name, e.g. "Arch Linux" or "macOS 15.1".
func OSName() string {
	switch runtime.GOOS {
	case "linux":
		if f, err := os.Open("/etc/os-release"); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
					return strings.Trim(v, `"'`)
				}
			}
		}
		return "Linux"
	case "darwin":
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
		return "macOS"
	case "windows":
		return "Windows"
	}
	return runtime.GOOS
}

// Timezone is the PC's IANA time zone name (e.g. Europe/Budapest), or ""
// if it can't be told. The server uses it to know when a backup is late.
func Timezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if runtime.GOOS != "windows" {
		if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
			if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
				return name
			}
		}
		if b, err := os.ReadFile("/etc/timezone"); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

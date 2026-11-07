package agent

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strings"
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

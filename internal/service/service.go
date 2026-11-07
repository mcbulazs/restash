// Package service installs the agent so it runs in the background and
// starts by itself: a systemd user service on Linux, a launchd agent on
// macOS (both run as the person, while they're logged in, and on Linux
// also after a reboot), and a Windows service.
package service

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Name is the service's name everywhere.
const Name = "restash-agent"

// Args are what the service runs the agent with.
var Args = []string{"run", "-service"}

// Install copies the running binary to BinaryPath and installs and starts
// the service, replacing an older one. keep says to leave the binary
// already at BinaryPath (e.g. it updated itself to something newer). It
// returns where the binary is.
func Install(keep func(installed string) bool) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return "", err
	}
	dst, err := BinaryPath()
	if err != nil {
		return "", err
	}
	if err := stopIfRunning(); err != nil {
		return "", err
	}
	if _, err := os.Stat(dst); err == nil && keep != nil && keep(dst) {
		self = dst
	}
	if !sameFile(self, dst) {
		if err := copyBinary(self, dst); err != nil {
			return "", fmt.Errorf("copying the agent to %s: %w", dst, err)
		}
	}
	if err := install(dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Uninstall stops and removes the service and the installed binary. The
// config (the PC's link) stays.
func Uninstall() error {
	if err := uninstall(); err != nil {
		return err
	}
	dst, err := BinaryPath()
	if err != nil {
		return err
	}
	removeBinary(dst)
	return nil
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// copyBinary copies src to dst through a temporary file, so a half-written
// binary never sits at dst.
func copyBinary(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return Replace(tmp, dst)
}

// Replace puts the file at src in place of dst. A running binary can't be
// overwritten on Windows but can be renamed, so the old one moves aside to
// dst.old first.
func Replace(src, dst string) error {
	old := dst + ".old"
	os.Remove(old)
	if err := os.Rename(dst, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return nil
}

func removeBinary(dst string) {
	os.Remove(dst)
	os.Remove(dst + ".old")
}

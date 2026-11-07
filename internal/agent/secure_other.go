//go:build !windows

package agent

// secureDir is a no-op here: the folder is created 0700.
func secureDir(string) error { return nil }

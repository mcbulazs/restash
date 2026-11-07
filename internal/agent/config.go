// Package agent is the PC side of restash: joining, unlocking the PC's key,
// and staying linked to the server.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mcbulazs/restash/internal/restic"
)

// Config is this PC's link to restash. It holds secrets, so it is kept in a
// file only the current user can read.
type Config struct {
	Server string `json:"server"` // e.g. https://restash.bulazs.com
	PCID   string `json:"pc_id"`
	Token  string `json:"token"`
	// RepoKey is the unlocked restic password; empty until unlocked.
	RepoKey string `json:"repo_key,omitempty"`
	// Home is the person's home folder, for when the agent runs as a
	// system service (Windows) whose own home is somewhere else.
	Home string `json:"home,omitempty"`
}

// HomeDir is the person's home folder: Home if set, else the current user's.
func (c *Config) HomeDir() string {
	if c != nil && c.Home != "" {
		return c.Home
	}
	h, _ := os.UserHomeDir()
	return h
}

// DefaultPath is <user config dir>/restash/agent.json; on Windows, where
// the agent runs as a system service, it's %ProgramData%\restash\agent.json.
func DefaultPath() (string, error) {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			return "", errors.New("%ProgramData% isn't set")
		}
		return filepath.Join(pd, "restash", "agent.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "restash", "agent.json"), nil
}

// StatePath is where the agent keeps its run times, next to the config.
func StatePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "state.json")
}

// ErrNotJoined means this PC hasn't been linked yet.
var ErrNotJoined = errors.New("this PC isn't linked to restash yet: run `restash-agent join <code>`")

// Load reads the config at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotJoined
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if c.PCID == "" || c.Token == "" {
		return nil, ErrNotJoined
	}
	return &c, CheckServer(c.Server)
}

// Save writes the config to path, readable only by the current user.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := secureDir(filepath.Dir(path)); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CheckServer makes sure the server URL is https (plain http is allowed only
// for this machine, for testing).
func CheckServer(server string) error {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" {
		return fmt.Errorf("server %q is not a site address like https://restash.example.com", server)
	}
	if u.Scheme == "http" && !isLocal(u.Hostname()) {
		return fmt.Errorf("server %q must use https", server)
	}
	return nil
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Repo is how restic reaches this PC's repo: the username is the PC id and
// the password its token.
func (c *Config) Repo() restic.Repo {
	return restic.Repo{
		URL:      strings.TrimSuffix(c.Server, "/") + "/repo/" + c.PCID + "/",
		Username: c.PCID,
		Password: c.Token,
		Key:      c.RepoKey,
	}
}

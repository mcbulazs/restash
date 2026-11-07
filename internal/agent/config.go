// Package agent holds the PC agent's local settings.
package agent

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mcbulazs/restash/internal/restic"
)

// Config is what the agent needs to reach its repo. Until accounts exist it
// is kept in a file only the current user can read.
type Config struct {
	Server   string `json:"server"`    // e.g. https://restash.bulazs.com
	RepoPath string `json:"repo_path"` // e.g. anna or anna/laptop
	Username string `json:"username"`
	Password string `json:"password"`
	Key      string `json:"key"`
}

// DefaultPath is <user config dir>/restash/agent.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "restash", "agent.json"), nil
}

// Load reads the config at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no config at %s: run `restash-agent init` first", path)
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &c, c.Validate()
}

// Save writes the config to path, readable only by the current user.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

// Validate checks the fields needed to open the repo.
func (c *Config) Validate() error {
	u, err := url.Parse(c.Server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("server %q is not an http(s) URL", c.Server)
	}
	if u.Scheme == "http" && !isLocal(u.Hostname()) {
		return fmt.Errorf("server %q must use https", c.Server)
	}
	if c.Username == "" || c.Password == "" || c.Key == "" {
		return errors.New("username, password and key are required")
	}
	if c.RepoPath != c.Username && !strings.HasPrefix(c.RepoPath, c.Username+"/") {
		return fmt.Errorf("repo path %q must start with the username %q", c.RepoPath, c.Username)
	}
	return nil
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Repo turns the config into restic's view of the repo.
func (c *Config) Repo() restic.Repo {
	return restic.Repo{
		URL:      strings.TrimSuffix(c.Server, "/") + "/repo/" + strings.Trim(c.RepoPath, "/") + "/",
		Username: c.Username,
		Password: c.Password,
		Key:      c.Key,
	}
}

// NewKey makes a random repo password (256 bits).
func NewKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

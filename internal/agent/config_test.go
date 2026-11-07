package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restash", "agent.json")
	c := &Config{Server: "https://restash.example.com/", RepoPath: "anna/laptop", Username: "anna", Password: "p", Key: NewKey()}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo().URL != "https://restash.example.com/repo/anna/laptop/" {
		t.Fatalf("url %s", got.Repo().URL)
	}
}

func TestValidate(t *testing.T) {
	ok := Config{Server: "https://x.com", RepoPath: "anna", Username: "anna", Password: "p", Key: "k"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	local := ok
	local.Server = "http://localhost:8080"
	if err := local.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"plain http":   func(c *Config) { c.Server = "http://x.com" },
		"no key":       func(c *Config) { c.Key = "" },
		"other user":   func(c *Config) { c.RepoPath = "bob" },
		"prefix trick": func(c *Config) { c.RepoPath = "annabel" },
		"not a URL":    func(c *Config) { c.Server = "x.com" },
	} {
		c := ok
		mod(&c)
		if c.Validate() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNewKeyIsRandom(t *testing.T) {
	if a, b := NewKey(), NewKey(); a == b || len(a) != 43 {
		t.Fatalf("%q %q", a, b)
	}
}

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restash", "agent.json")
	if _, err := Load(path); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("missing config: %v", err)
	}
	c := &Config{Server: "https://restash.example.com", PCID: "abc", Token: "tok", RepoKey: "key"}
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
	r := got.Repo()
	if r.URL != "https://restash.example.com/repo/abc/" || r.Username != "abc" || r.Password != "tok" || r.Key != "key" {
		t.Fatalf("repo %+v", r)
	}
}

func TestCheckServer(t *testing.T) {
	for _, ok := range []string{"https://x.com", "http://localhost:8080", "http://127.0.0.1:1"} {
		if err := CheckServer(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://x.com", "x.com", "https://x.com/path", "ftp://x.com"} {
		if CheckServer(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestWSURL(t *testing.T) {
	if wsURL("https://a.com") != "wss://a.com" || wsURL("http://localhost:1") != "ws://localhost:1" {
		t.Fatal("wsURL")
	}
}

func TestLoadNoPermission(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	os.WriteFile(path, []byte(`{}`), 0o000)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "can't read") {
		t.Fatalf("got %v", err)
	}
}

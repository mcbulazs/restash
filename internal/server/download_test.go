package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mcbulazs/restash/internal/proto"
)

func TestAgentDownload(t *testing.T) {
	e := newEnv(t)
	setup, _ := e.store.SetupInvite(context.Background())
	anna := e.client()
	e.signup(anna, setup.Token, "anna@example.com", "hunter2 hunter2")

	// A stand-in for the GitHub release.
	bins := map[string][]byte{
		"restash-agent-windows-amd64.exe": []byte("MZ windows agent"),
		"restash-agent-darwin-arm64":      []byte("mac agent"),
		"restash-agent-linux-amd64":       []byte("linux agent"),
	}
	var sums bytes.Buffer
	for name, b := range bins {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	fetches := 0
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:]
		if name == "SHA256SUMS" {
			w.Write(sums.Bytes())
			return
		}
		if name == "restash-agent-linux-amd64" {
			fetches++
		}
		if b, ok := bins[name]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer release.Close()
	e.server.agents = newAgentFiles(release.URL+"/", t.TempDir())

	var jc joinCodeJSON
	e.call(anna, "POST", "/api/pcs/join", nil, &jc)
	get := func(c *http.Client, target string) (*http.Response, []byte) {
		resp, err := c.Get(e.srv.URL + "/api/pcs/join/" + jc.Code + "/download/" + target)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}

	resp, body := get(anna, "windows-amd64")
	if resp.StatusCode != 200 || string(body) != "MZ windows agent" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="restash-setup-`+jc.Code+`.exe"` {
		t.Fatalf("windows: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Disposition"))
	}
	resp, body = get(anna, "linux-amd64")
	if resp.StatusCode != 200 || string(body) != "linux agent" || resp.Header.Get("Content-Disposition") != `attachment; filename="restash-setup-`+jc.Code+`"` {
		t.Fatalf("linux: %d %q", resp.StatusCode, body)
	}
	get(anna, "linux-amd64")
	if fetches != 1 {
		t.Fatalf("fetched the linux agent %d times; should be cached", fetches)
	}

	// macOS: a zip holding an executable named for the code.
	resp, body = get(anna, "darwin-arm64")
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("darwin zip: %v", err)
	}
	zf := zr.File[0]
	rc, _ := zf.Open()
	inner, _ := io.ReadAll(rc)
	if zf.Name != "restash-setup-"+jc.Code || zf.Mode().Perm() != 0o755 || string(inner) != "mac agent" {
		t.Fatalf("zip entry %s %v %q", zf.Name, zf.Mode(), inner)
	}

	// Not in the release, not a build, someone else's code.
	if resp, _ := get(anna, "darwin-amd64"); resp.StatusCode != 502 {
		t.Fatalf("missing asset: %d", resp.StatusCode)
	}
	if resp, _ := get(anna, "plan9-386"); resp.StatusCode != 404 {
		t.Fatalf("bad target: %d", resp.StatusCode)
	}
	if resp, _ := get(e.client(), "linux-amd64"); resp.StatusCode != 401 {
		t.Fatalf("logged out: %d", resp.StatusCode)
	}

	// A tampered binary is refused.
	bins["restash-agent-windows-arm64.exe"] = []byte("evil")
	sums.WriteString("0000000000000000000000000000000000000000000000000000000000000000  restash-agent-windows-arm64.exe\n")
	e.server.agents = newAgentFiles(release.URL+"/", t.TempDir())
	if resp, body := get(anna, "windows-arm64"); resp.StatusCode != 502 {
		t.Fatalf("tampered: %d %s", resp.StatusCode, body)
	}

	// Once the code is used, its download is gone.
	if s := e.call(&http.Client{}, "POST", "/api/agent/join", proto.JoinRequest{Code: jc.Code, Name: "pc", OS: "linux"}, nil); s != 200 {
		t.Fatalf("join: %d", s)
	}
	if resp, _ := get(anna, "linux-amd64"); resp.StatusCode != 410 {
		t.Fatalf("used code: %d", resp.StatusCode)
	}
}

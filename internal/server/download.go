package server

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mcbulazs/restash/internal/store"
)

// The "Add a PC" download is the agent binary from the GitHub release,
// named restash-setup-<code> so it knows which account to join. The bytes
// are the release's own (checked against its SHA256SUMS), the same for
// everyone; only the name differs.

// agentTargets are the builds a release has.
var agentTargets = map[string]bool{
	"linux-amd64": true, "linux-arm64": true,
	"windows-amd64": true, "windows-arm64": true,
	"darwin-amd64": true, "darwin-arm64": true,
}

// DefaultAgentSource is where agents for version come from: that release,
// or the newest one for development builds.
func DefaultAgentSource(version string) string {
	if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(version) {
		return "https://github.com/mcbulazs/restash/releases/download/" + version + "/"
	}
	return "https://github.com/mcbulazs/restash/releases/latest/download/"
}

type agentFiles struct {
	source string // release download URL, or a local folder
	cache  string // folder for downloaded binaries, by hash
	client *http.Client

	mu      sync.Mutex
	sums    map[string]string // asset -> sha256
	sumsAt  time.Time
	fetches map[string]*sync.Mutex
}

func newAgentFiles(source, cache string) *agentFiles {
	return &agentFiles{source: source, cache: cache, client: &http.Client{Timeout: 5 * time.Minute}, fetches: map[string]*sync.Mutex{}}
}

func (a *agentFiles) local() bool {
	return a.source != "" && !strings.HasPrefix(a.source, "https://") && !strings.HasPrefix(a.source, "http://")
}

func (a *agentFiles) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "restash-server")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// sum is asset's hash from the release's SHA256SUMS (re-read every 10
// minutes, so "latest" follows new releases).
func (a *agentFiles) sum(ctx context.Context, asset string) (string, error) {
	a.mu.Lock()
	if a.sums != nil && time.Since(a.sumsAt) < 10*time.Minute {
		h, ok := a.sums[asset]
		a.mu.Unlock()
		if !ok {
			return "", fmt.Errorf("%s isn't in the release", asset)
		}
		return h, nil
	}
	a.mu.Unlock()
	resp, err := a.get(ctx, a.source+"SHA256SUMS")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && len(f[0]) == 64 {
			sums[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	a.mu.Lock()
	a.sums, a.sumsAt = sums, time.Now()
	a.mu.Unlock()
	h, ok := sums[asset]
	if !ok {
		return "", fmt.Errorf("%s isn't in the release", asset)
	}
	return h, nil
}

// open returns the agent binary for asset, downloading and checking it the
// first time.
func (a *agentFiles) open(ctx context.Context, asset string) (*os.File, error) {
	if a.local() {
		return os.Open(filepath.Join(a.source, asset))
	}
	want, err := a.sum(ctx, asset)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(a.cache, want)
	if f, err := os.Open(path); err == nil {
		return f, nil
	}
	a.mu.Lock()
	m := a.fetches[want]
	if m == nil {
		m = &sync.Mutex{}
		a.fetches[want] = m
	}
	a.mu.Unlock()
	m.Lock()
	defer m.Unlock()
	if f, err := os.Open(path); err == nil {
		return f, nil // another request fetched it meanwhile
	}
	if err := os.MkdirAll(a.cache, 0o700); err != nil {
		return nil, err
	}
	resp, err := a.get(ctx, a.source+asset)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(a.cache, "fetch-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 512<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return nil, errors.New("the downloaded agent doesn't match the release's SHA256SUMS")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

// handleAgentDownload serves the agent named for a join code:
// restash-setup-K7Q2MX.exe, a .zip holding restash-setup-K7Q2MX on macOS
// (so it stays runnable), or the plain file on Linux.
func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	code := store.NormalizeJoinCode(r.PathValue("code"))
	target := r.PathValue("target")
	if !agentTargets[target] {
		httpError(w, http.StatusNotFound, "no such build")
		return
	}
	pc, err := s.store.JoinStatus(r.Context(), userFrom(r.Context()).ID, code)
	switch {
	case isNotFound(err):
		httpError(w, http.StatusGone, "this code expired; get a new one on the Add a PC page")
		return
	case err != nil:
		internalError(w, r, err)
		return
	case pc != nil:
		httpError(w, http.StatusGone, "this code was already used; get a new one on the Add a PC page")
		return
	}
	osName := target[:strings.IndexByte(target, '-')]
	asset := "restash-agent-" + target
	if osName == "windows" {
		asset += ".exe"
	}
	f, err := s.agents.open(r.Context(), asset)
	if err != nil {
		httpError(w, http.StatusBadGateway, "couldn't get the agent: "+err.Error())
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		internalError(w, r, err)
		return
	}
	name := "restash-setup-" + code
	w.Header().Set("Cache-Control", "no-store")
	switch osName {
	case "darwin":
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
		zw := zip.NewWriter(w)
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fi.ModTime()}
		hdr.SetMode(0o755)
		zf, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(zf, f)
		}
		if err == nil {
			err = zw.Close()
		}
		if err != nil {
			slog.Warn("agent download cut short", "err", err)
		}
	default:
		if osName == "windows" {
			name += ".exe"
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		http.ServeContent(w, r, "", fi.ModTime(), f)
	}
}

// Package trash sits in front of rest-server and turns deletes into moves to a
// trash folder, so a broken or hacked PC can't wipe its own backup history.
// Files stay in the trash for a set number of days before the janitor removes
// them for good. Everything else is passed through to rest-server untouched.
package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Proxy forwards restic's REST traffic to rest-server. Callers must check
// the login and that the path belongs to the caller before it gets here.
type Proxy struct {
	repos   string // folder rest-server serves the repos from
	trash   string // folder deleted files are moved to
	forward *httputil.ReverseProxy
	now     func() time.Time
}

// NewProxy builds a proxy for the rest-server at upstream. repos and trash
// must be on the same filesystem (and the same mount) so moves are renames.
func NewProxy(upstream *url.URL, repos, trash string) *Proxy {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("rest-server unreachable", "err", err)
			http.Error(w, "storage unavailable", http.StatusBadGateway)
		},
	}
	return &Proxy{repos: repos, trash: trash, forward: rp, now: time.Now}
}

// File types in a restic repo, see the REST backend spec. Locks are
// short-lived bookkeeping, so they are really deleted, not trashed.
var trashedTypes = map[string]bool{
	"data": true, "keys": true, "snapshots": true, "index": true,
}

var (
	idPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
)

// object is a single file in a repo, as addressed by a DELETE request.
type object struct {
	repo []string // repo path segments, e.g. ["anna", "laptop"]
	typ  string
	name string
}

// parseObject splits /<repo...>/<type>/<name>. It is strict on purpose: the
// result is turned into a filesystem path.
func parseObject(p string) (object, error) {
	if !strings.HasPrefix(p, "/") {
		return object{}, errors.New("path must be absolute")
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 3 {
		return object{}, errors.New("not a repo file")
	}
	o := object{
		repo: parts[:len(parts)-2],
		typ:  parts[len(parts)-2],
		name: parts[len(parts)-1],
	}
	for _, s := range o.repo {
		if !segmentPattern.MatchString(s) {
			return object{}, fmt.Errorf("bad repo path segment %q", s)
		}
	}
	if o.typ != "locks" && !trashedTypes[o.typ] {
		return object{}, fmt.Errorf("unknown file type %q", o.typ)
	}
	if !idPattern.MatchString(o.name) {
		return object{}, errors.New("bad file name")
	}
	return o, nil
}

// file is where rest-server keeps the object, relative to the repos folder.
// Pack files are spread over subfolders named after their first two characters.
func (o object) file() string {
	parts := append([]string{}, o.repo...)
	parts = append(parts, o.typ)
	if o.typ == "data" {
		parts = append(parts, o.name[:2])
	}
	return filepath.Join(append(parts, o.name)...)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		p.forward.ServeHTTP(w, r)
		return
	}
	if r.URL.RawPath != "" {
		http.Error(w, "encoded paths are not allowed", http.StatusBadRequest)
		return
	}
	o, err := parseObject(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if o.typ == "locks" {
		p.forward.ServeHTTP(w, r)
		return
	}

	if err := p.moveToTrash(o); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Error("moving file to trash", "file", o.file(), "err", err)
		http.Error(w, "could not delete", http.StatusInternalServerError)
		return
	}
	slog.Info("moved to trash", "repo", strings.Join(o.repo, "/"), "type", o.typ, "name", o.name)
	w.WriteHeader(http.StatusOK)
}

// moveToTrash renames the file into trash/<day>/<repo...>/<type>/<name>.
func (p *Proxy) moveToTrash(o object) error {
	src := filepath.Join(p.repos, o.file())
	day := p.now().UTC().Format(dayLayout)
	dstDir := filepath.Join(append([]string{p.trash, day}, append(o.repo, o.typ)...)...)
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return err
	}
	if err := os.Rename(src, filepath.Join(dstDir, o.name)); err != nil {
		return err
	}
	syncDir(filepath.Dir(src))
	syncDir(dstDir)
	return nil
}

// syncDir makes a rename durable. Errors are ignored: the rename already
// happened and some filesystems don't support syncing folders.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		f.Sync()
		f.Close()
	}
}

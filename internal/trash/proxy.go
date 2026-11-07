// Package trash serves restic repos (with restic's own rest-server code,
// running inside restash) and turns deletes into moves to a trash folder,
// so a broken or hacked PC can't wipe its own backup history. Files stay in
// the trash for a set number of days before the janitor removes them for
// good. Everything else goes to rest-server untouched.
package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	restserver "github.com/restic/rest-server"
)

// Proxy serves restic's REST protocol for the repos folder. Callers must
// check the login and that the path belongs to the caller before it gets
// here; rest-server itself checks nothing.
type Proxy struct {
	repos   string // folder the repos are in
	trash   string // folder deleted files are moved to
	forward http.Handler
	now     func() time.Time
}

// NewProxy serves the repos in repos. repos and trash must be on the same
// filesystem (and the same mount) so moves are renames.
func NewProxy(repos, trash string) (*Proxy, error) {
	if err := os.MkdirAll(repos, 0o700); err != nil {
		return nil, err
	}
	h, err := restserver.NewHandler(&restserver.Server{
		Path:   repos,
		NoAuth: true, // restash checks every request first
	})
	if err != nil {
		return nil, err
	}
	return NewProxyFor(h, repos, trash), nil
}

// NewProxyFor is NewProxy with h in place of rest-server (for tests).
func NewProxyFor(h http.Handler, repos, trash string) *Proxy {
	return &Proxy{repos: repos, trash: trash, forward: h, now: time.Now}
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
		rec := &statusRecorder{ResponseWriter: w}
		p.forward.ServeHTTP(rec, r)
		if rec.status >= 500 {
			// rest-server logs the cause just before this.
			slog.Error("storage request failed: check that restash can write its storage folder",
				"method", r.Method, "path", r.URL.Path, "status", rec.status, "repos", p.repos)
		}
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

// statusRecorder notes the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush passes through, so streamed answers aren't held back.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// TrashRepo moves a whole repo (a removed PC's) into today's trash, as
// trash/<day>/removed-<name>-<time>. It's emptied with the rest, so the
// removal can be undone until then by moving the folder back. A repo
// that doesn't exist is fine.
func (p *Proxy) TrashRepo(name string) error {
	if !segmentPattern.MatchString(name) {
		return fmt.Errorf("bad repo name %q", name)
	}
	src := filepath.Join(p.repos, name)
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	now := p.now().UTC()
	dayDir := filepath.Join(p.trash, now.Format(dayLayout))
	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(dayDir, "removed-"+name+"-"+now.Format("150405"))
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	syncDir(p.repos)
	syncDir(dayDir)
	slog.Info("moved a removed PC's repo to the trash", "repo", name, "to", dst)
	return nil
}

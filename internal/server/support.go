package server

import (
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// repoAuth checks the agent's login on restic's requests: the username is
// the PC id, the password its token, and it may only touch /<pc id>/.
func (s *Server) repoAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, token, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="restash"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		pc, err := s.store.AuthPC(r.Context(), id, token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.RawPath != "" || !strings.HasPrefix(r.URL.Path, "/"+pc.ID+"/") || path.Clean(r.URL.Path) != strings.TrimSuffix(r.URL.Path, "/") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// webHandler serves the built web UI. Unknown paths get index.html so the
// app's own routes work on reload.
func webHandler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(dist, name); err == nil && !st.IsDir() {
				// Built files have a content hash in their name.
				if strings.Contains(name, "-") && (strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") || strings.HasSuffix(name, ".woff2")) {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, dist, "index.html")
	})
}

// limiter allows a number of events per key in a time window.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}, now: time.Now} }

func (l *limiter) allow(key string, n int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= n {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	if len(l.hits) > 10000 { // don't grow forever
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Hour {
				delete(l.hits, k)
			}
		}
	}
	return true
}

// sizeCache remembers how much space each PC's repo takes.
type sizeCache struct {
	dir string
	ttl time.Duration
	mu  sync.Mutex
	m   map[string]sizeEntry
}

type sizeEntry struct {
	size int64
	at   time.Time
}

func newSizeCache(dir string, ttl time.Duration) *sizeCache {
	return &sizeCache{dir: dir, ttl: ttl, m: map[string]sizeEntry{}}
}

func (c *sizeCache) size(id string) int64 {
	c.mu.Lock()
	e, ok := c.m[id]
	c.mu.Unlock()
	if ok && time.Since(e.at) < c.ttl {
		return e.size
	}
	var total int64
	if c.dir != "" {
		filepath.WalkDir(filepath.Join(c.dir, id), func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if info, err := d.Info(); err == nil {
					total += info.Size()
				}
			}
			return nil
		})
	}
	c.mu.Lock()
	c.m[id] = sizeEntry{total, time.Now()}
	c.mu.Unlock()
	return total
}

func (c *sizeCache) forget(id string) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

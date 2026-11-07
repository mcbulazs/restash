package trash

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeRestServer answers HEAD with headStatus and records other requests.
type fakeRestServer struct {
	headStatus int
	seen       []string
	auth       string
}

func (f *fakeRestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.seen = append(f.seen, r.Method+" "+r.URL.Path)
	f.auth = r.Header.Get("Authorization")
	if r.Method == http.MethodHead {
		w.WriteHeader(f.headStatus)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func setup(t *testing.T, headStatus int) (*Proxy, *fakeRestServer, string) {
	t.Helper()
	root := t.TempDir()
	fake := &fakeRestServer{headStatus: headStatus}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	p := NewProxy(u, filepath.Join(root, "repos"), filepath.Join(root, "trash"))
	p.now = func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }
	return p, fake, root
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func del(p *Proxy, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.SetBasicAuth("anna", "secret")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestDeleteMovesPackToTrash(t *testing.T) {
	p, fake, root := setup(t, http.StatusOK)
	src := filepath.Join(root, "repos", "anna", "laptop", "data", id[:2], id)
	writeFile(t, src)

	rec := del(p, "/anna/laptop/data/"+id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("file still in repo: %v", err)
	}
	dst := filepath.Join(root, "trash", "2026-11-07", "anna", "laptop", "data", id)
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("file not in trash: %v", err)
	}
	if len(fake.seen) != 1 || fake.seen[0] != "HEAD /anna/laptop/data/"+id {
		t.Fatalf("rest-server saw %v", fake.seen)
	}
	if !strings.HasPrefix(fake.auth, "Basic ") {
		t.Fatalf("credentials not passed on: %q", fake.auth)
	}
}

func TestDeleteSnapshotIsFlat(t *testing.T) {
	p, _, root := setup(t, http.StatusOK)
	writeFile(t, filepath.Join(root, "repos", "anna", "snapshots", id))
	if rec := del(p, "/anna/snapshots/"+id); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "trash", "2026-11-07", "anna", "snapshots", id)); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRefusedByRestServer(t *testing.T) {
	p, _, root := setup(t, http.StatusUnauthorized)
	src := filepath.Join(root, "repos", "bob", "snapshots", id)
	writeFile(t, src)
	if rec := del(p, "/bob/snapshots/"+id); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("file was touched: %v", err)
	}
}

func TestDeleteMissingFile(t *testing.T) {
	p, _, _ := setup(t, http.StatusOK)
	if rec := del(p, "/anna/index/"+id); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestLocksAreReallyDeleted(t *testing.T) {
	p, fake, _ := setup(t, http.StatusOK)
	if rec := del(p, "/anna/locks/"+id); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fake.seen) != 1 || fake.seen[0] != "DELETE /anna/locks/"+id {
		t.Fatalf("rest-server saw %v", fake.seen)
	}
}

func TestOtherRequestsPassThrough(t *testing.T) {
	p, fake, _ := setup(t, http.StatusOK)
	req := httptest.NewRequest(http.MethodPost, "/anna/data/"+id, strings.NewReader("pack"))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(fake.seen) != 1 || fake.seen[0] != "POST /anna/data/"+id {
		t.Fatalf("status %d, rest-server saw %v", rec.Code, fake.seen)
	}
}

func TestBadDeletePaths(t *testing.T) {
	p, fake, _ := setup(t, http.StatusOK)
	for _, path := range []string{
		"/anna/config",
		"/anna/data/notanid",
		"/data/" + id,
		"/../etc/data/" + id,
		"/.hidden/data/" + id,
		"/anna/secrets/" + id,
		"/anna/data/" + strings.ToUpper(id),
	} {
		if rec := del(p, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
	if len(fake.seen) != 0 {
		t.Fatalf("rest-server saw %v", fake.seen)
	}
}

func TestEmpty(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"2026-10-01", "2026-10-07", "2026-10-08", "2026-11-07", "not-a-day"} {
		writeFile(t, filepath.Join(dir, d, "anna", "data", id))
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	n, err := Empty(dir, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d days, want 2", n)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "2026-10-08,2026-11-07,not-a-day" {
		t.Fatalf("left %v", names)
	}
}

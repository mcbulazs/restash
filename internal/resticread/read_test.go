package resticread

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// restServer serves a local restic repo folder the way rest-server does,
// enough for reading: listings (v2 JSON) and files, with ranges.
func restServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.Trim(r.URL.Path, "/")
		parts := strings.Split(p, "/")
		if strings.HasSuffix(r.URL.Path, "/") {
			var items []map[string]any
			filepath.WalkDir(filepath.Join(dir, p), func(fp string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					info, _ := d.Info()
					items = append(items, map[string]any{"name": d.Name(), "size": info.Size()})
				}
				return nil
			})
			json.NewEncoder(w).Encode(items)
			return
		}
		fp := filepath.Join(dir, p)
		if len(parts) == 2 && parts[0] == "data" {
			fp = filepath.Join(dir, "data", parts[1][:2], parts[1])
		}
		f, err := os.Open(fp)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		http.ServeContent(w, r, "", time.Time{}, f)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const fixturePassword = "fixture-password"

func openFixture(t *testing.T) *Repo {
	t.Helper()
	dir := os.Getenv("RESTASH_TEST_REPO")
	if dir == "" {
		dir = "testdata/repo"
	}
	srv := restServer(t, dir)
	r, err := Open(context.Background(), srv.URL+"/", fixturePassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWrongPassword(t *testing.T) {
	dir := os.Getenv("RESTASH_TEST_REPO")
	if dir == "" {
		dir = "testdata/repo"
	}
	srv := restServer(t, dir)
	if _, err := Open(context.Background(), srv.URL, "wrong", Options{}); err == nil {
		t.Fatal("opened with the wrong password")
	}
}

func TestRead(t *testing.T) {
	ctx := context.Background()
	r := openFixture(t)
	snaps, err := r.Snapshots(ctx)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("%v %v", snaps, err)
	}
	s := &snaps[0]
	root := "/home/anna/src"
	if s.Root() != "/home/anna" || s.Hostname != "fixture" {
		t.Fatalf("root %q host %q", s.Root(), s.Hostname)
	}

	list, err := r.List(ctx, s, root+"/docs")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range list {
		names = append(names, n.Name+":"+n.Type)
	}
	if strings.Join(names, ",") != "sub:dir,a.txt:file,link:symlink" {
		t.Fatalf("docs lists %v", names)
	}

	n, err := r.Find(ctx, s, root+"/docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.WriteFile(ctx, n, &buf); err != nil || buf.String() != "hello world\n" {
		t.Fatalf("%q %v", buf.String(), err)
	}

	big, _ := r.Find(ctx, s, root+"/docs/sub/b.txt")
	buf.Reset()
	if err := r.WriteFile(ctx, big, &buf); err != nil || buf.Len() != 2000*len("second file\n") || uint64(buf.Len()) != big.Size {
		t.Fatalf("b.txt: %d bytes, %v", buf.Len(), err)
	}

	total, files, done, err := r.Size(ctx, s, []string{root}, 0)
	if err != nil || !done || files != 3 {
		t.Fatalf("size %d files %d done %v err %v", total, files, done, err)
	}

	found, err := r.Search(ctx, s, "/", "PHOTO", 10)
	if err != nil || len(found) != 1 || !strings.HasSuffix(found[0], "/pics/photo.bin") {
		t.Fatalf("search %v %v", found, err)
	}

	// A zip of docs has its files under docs/, with the right contents.
	buf.Reset()
	if err := r.WriteZip(ctx, s, root, []string{root + "/docs"}, &buf, nil); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		got[f.Name] = b.Len()
	}
	if got["docs/a.txt"] != 12 || got["docs/sub/b.txt"] != int(big.Size) {
		t.Fatalf("zip has %v", got)
	}
	if _, ok := got["docs/link"]; ok {
		t.Fatal("symlink went into the zip")
	}
}

func TestOriginal(t *testing.T) {
	win := &Snapshot{Paths: []string{`C:\Users\anna\Documents`, `C:\Users\anna\Pictures`}}
	if got := win.Original("/C/Users/anna/Documents/University/x.docx"); got != `C:\Users\anna\Documents\University\x.docx` {
		t.Fatal(got)
	}
	if got := win.Root(); got != "/C/Users/anna" {
		t.Fatal(got)
	}
	unix := &Snapshot{Paths: []string{"/home/anna/src"}}
	if got := unix.Original("/home/anna/src/x"); got != "/home/anna/src/x" {
		t.Fatal(got)
	}
	rel := &Snapshot{Paths: []string{"src"}}
	if got := rel.Original("/src/x"); got != "src/x" {
		t.Fatal(got)
	}
	two := &Snapshot{Paths: []string{"/home/anna/a", "/home/anna/b/c"}}
	if got := two.Root(); got != "/home/anna" {
		t.Fatal(got)
	}
}

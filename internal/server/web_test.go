package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWebHandler(t *testing.T) {
	h := webHandler(fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"sw.js":         {Data: []byte("worker")},
		"main-ABC12.js": {Data: []byte("app code")},
	})
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		return rec
	}
	if r := get("/sw.js"); r.Code != 200 || r.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(r.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("sw.js: %d %v", r.Code, r.Header())
	}
	if r := get("/main-ABC12.js"); !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("hashed file: %v", r.Header())
	}
	if r := get("/pcs/abc"); r.Code != 200 || !strings.Contains(r.Body.String(), "app") {
		t.Fatalf("app route: %d %q", r.Code, r.Body)
	}
	// A download the worker didn't answer: not the app page in a hidden frame.
	if r := get("/dl/0123/x.zip"); r.Code != http.StatusNotFound || strings.Contains(r.Body.String(), "<html>") {
		t.Fatalf("/dl/: %d %q", r.Code, r.Body)
	}
}

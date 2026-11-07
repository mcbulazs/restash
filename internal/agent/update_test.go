package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.1.0", "v1.2.0", false},
		{"v2.0.0", "dev", false},
		{"v1.2", "v1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestUpdate(t *testing.T) {
	newBin := []byte("new agent")
	sum := sha256.Sum256(newBin)
	sums := hex.EncodeToString(sum[:]) + "  " + AssetName() + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v1.3.0"}`) })
	mux.HandleFunc("/download/v1.3.0/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sums) })
	mux.HandleFunc("/download/v1.3.0/"+AssetName(), func(w http.ResponseWriter, r *http.Request) { w.Write(newBin) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "restash-agent")
	os.WriteFile(exe, []byte("old agent"), 0o755)
	u := &Updater{Current: "v1.2.0", Exe: exe, API: srv.URL + "/latest", Download: srv.URL + "/download/"}
	ctx := context.Background()

	v, err := u.Check(ctx)
	if err != nil || v != "v1.3.0" {
		t.Fatalf("Check = %q, %v", v, err)
	}
	if err := u.Apply(ctx, v); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new agent" {
		t.Fatalf("binary is %q", b)
	}

	// A tampered download is refused and the binary stays.
	newBin = []byte("evil agent")
	os.WriteFile(exe, []byte("old agent"), 0o755)
	if err := u.Apply(ctx, v); err == nil {
		t.Fatal("tampered download accepted")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old agent" {
		t.Fatalf("binary is %q after a refused update", b)
	}

	// Development builds don't update.
	u.Current = "dev"
	if v, _ := u.Check(ctx); v != "" {
		t.Fatalf("dev build wants %s", v)
	}
}

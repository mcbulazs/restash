package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithLabel(t *testing.T) {
	cases := map[string]string{
		"thesis.docx": "thesis (restored Nov 7).docx",
		"Makefile":    "Makefile (restored Nov 7)",
		".bashrc":     ".bashrc (restored Nov 7)",
	}
	for in, want := range cases {
		if got := withLabel(in, "restored Nov 7", false); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if got := withLabel("photos.2024", "restored Nov 7", true); got != "photos.2024 (restored Nov 7)" {
		t.Error(got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if uniquePath(p) != p {
		t.Fatal("free name changed")
	}
	os.WriteFile(p, nil, 0o600)
	if got := uniquePath(p); got != filepath.Join(dir, "a 2.txt") {
		t.Fatal(got)
	}
	os.Mkdir(filepath.Join(dir, "Restored Nov 7"), 0o700)
	if got := uniquePath(filepath.Join(dir, "Restored Nov 7")); got != filepath.Join(dir, "Restored Nov 7 2") {
		t.Fatal(got)
	}
}

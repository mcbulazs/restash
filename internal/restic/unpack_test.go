package restic

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func pack(b []byte) ([]byte, string) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	sum := sha256.Sum256(b)
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func TestUnpack(t *testing.T) {
	dir := t.TempDir()
	bin := []byte("#!/bin/sh\necho restic\n")
	gz, hash := pack(bin)
	p, err := unpack(dir, gz, hash, "0.19.1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(bin) {
		t.Fatal("wrong contents")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("not executable: %v", fi.Mode())
	}

	// A damaged copy is replaced.
	os.WriteFile(p, []byte("damaged"), 0o700)
	if p2, err := unpack(dir, gz, hash, "0.19.1"); err != nil || p2 != p {
		t.Fatal(p2, err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(bin) {
		t.Fatal("damaged copy kept")
	}

	// Contents that don't match the checksum are refused.
	bad, _ := pack([]byte("something else"))
	if _, err := unpack(t.TempDir(), bad, hash, "0.19.1"); err == nil {
		t.Fatal("mismatched contents accepted")
	}

	// A new version replaces the old one.
	gz2, hash2 := pack([]byte("newer"))
	p3, err := unpack(dir, gz2, hash2, "0.20.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("old version left behind")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 || left[0] != p3 {
		t.Fatalf("left %v", left)
	}
}

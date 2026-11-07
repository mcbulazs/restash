package restic

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// unpack writes the gzipped program into dir as restic-<version>-<hash>,
// unless an intact copy is already there, and removes copies of other
// versions. wantHash is the SHA-256 of the unpacked program; a damaged or
// swapped file is noticed and replaced.
func unpack(dir string, gz []byte, wantHash, version string) (string, error) {
	want, err := hex.DecodeString(wantHash)
	if err != nil || len(want) != sha256.Size {
		return "", fmt.Errorf("built-in restic has a bad checksum %q", wantHash)
	}
	name := fmt.Sprintf("restic-%s-%s", version, wantHash[:12])
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if ok, _ := sameHash(path, want); ok {
		return path, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".restic-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("unpacking restic: %w", err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), zr)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("unpacking restic: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return "", fmt.Errorf("built-in restic doesn't match its checksum")
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil {
		return "", err
	}
	os.Remove(path) // Windows can't rename over an existing file
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}

	// Old versions are no longer needed.
	if old, err := filepath.Glob(filepath.Join(dir, "restic-*")); err == nil {
		for _, o := range old {
			if o != path && strings.HasPrefix(filepath.Base(o), "restic-") {
				os.Remove(o)
			}
		}
	}
	return path, nil
}

func sameHash(path string, want []byte) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return bytes.Equal(h.Sum(nil), want), nil
}

package resticread

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Repo is an opened restic repository.
type Repo struct {
	base   string // e.g. https://restash.example.com/repo/<pc>/
	client *http.Client
	header http.Header
	key    *key
	ver    int

	idxOnce sync.Once
	idxErr  error
	index   map[string]blobRef // blob id → where it is

	cacheMu sync.Mutex
	trees   map[string]*Tree
}

type blobRef struct {
	pack         string
	offset       uint32
	length       uint32
	uncompressed uint32
}

// Options set how the repo is reached.
type Options struct {
	Client *http.Client // defaults to http.DefaultClient
	Header http.Header  // extra headers, e.g. Authorization
}

var zdec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))

// Open unlocks the repo at base (a REST URL ending in /) with password.
func Open(ctx context.Context, base, password string, opt Options) (*Repo, error) {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	r := &Repo{base: base, client: opt.Client, header: opt.Header, trees: map[string]*Tree{}}
	if r.client == nil {
		r.client = http.DefaultClient
	}
	names, err := r.list(ctx, "keys")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		raw, err := r.get(ctx, "keys/"+n, 0, 0)
		if err != nil {
			return nil, err
		}
		if k, err := unlockKeyFile(raw, password); err == nil {
			r.key = k
			break
		}
	}
	if r.key == nil {
		return nil, errors.New("the key doesn't open this backup")
	}
	raw, err := r.get(ctx, "config", 0, 0)
	if err != nil {
		return nil, err
	}
	plain, err := r.openFile(raw)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, err
	}
	if cfg.Version != 1 && cfg.Version != 2 {
		return nil, fmt.Errorf("unsupported repo version %d", cfg.Version)
	}
	r.ver = cfg.Version
	return r, nil
}

// get fetches a file, or a byte range of it when length > 0.
func (r *Repo) get(ctx context.Context, name string, offset, length int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+name, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	if length > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("reading %s: %s", name, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if length > 0 && resp.StatusCode == http.StatusOK {
		// The server ignored the range; cut it out ourselves.
		if offset+length > int64(len(b)) {
			return nil, fmt.Errorf("reading %s: file too short", name)
		}
		b = b[offset : offset+length]
	}
	if length > 0 && int64(len(b)) != length {
		return nil, fmt.Errorf("reading %s: got %d bytes, want %d", name, len(b), length)
	}
	return b, nil
}

// list returns the file names in one of the repo's folders.
func (r *Repo) list(ctx context.Context, dir string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+dir+"/", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/vnd.x.restic.rest.v2")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing %s: %s", dir, resp.Status)
	}
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if isID(it.Name) {
			out = append(out, it.Name)
		}
	}
	return out, nil
}

func isID(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// openFile decrypts a whole file (config, index, snapshot) and unpacks it
// if it is compressed.
func (r *Repo) openFile(raw []byte) ([]byte, error) {
	plain, err := r.key.open(raw)
	if err != nil {
		return nil, err
	}
	if r.ver >= 2 && len(plain) > 0 && plain[0] == 2 {
		return zdec.DecodeAll(plain[1:], nil)
	}
	return plain, nil
}

// Snapshot is one backup.
type Snapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Tree     string    `json:"tree"`
	Paths    []string  `json:"paths"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
}

// Snapshots lists the repo's backups, newest first.
func (r *Repo) Snapshots(ctx context.Context) ([]Snapshot, error) {
	names, err := r.list(ctx, "snapshots")
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(names))
	for _, n := range names {
		raw, err := r.get(ctx, "snapshots/"+n, 0, 0)
		if err != nil {
			return nil, err
		}
		plain, err := r.openFile(raw)
		if err != nil {
			return nil, err
		}
		var s Snapshot
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		s.ID = n
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

type indexFile struct {
	Packs []struct {
		ID    string `json:"id"`
		Blobs []struct {
			ID           string `json:"id"`
			Offset       uint32 `json:"offset"`
			Length       uint32 `json:"length"`
			Uncompressed uint32 `json:"uncompressed_length"`
		} `json:"blobs"`
	} `json:"packs"`
}

// loadIndex reads all index files once.
func (r *Repo) loadIndex(ctx context.Context) error {
	r.idxOnce.Do(func() {
		names, err := r.list(ctx, "index")
		if err != nil {
			r.idxErr = err
			return
		}
		idx := map[string]blobRef{}
		for _, n := range names {
			raw, err := r.get(ctx, "index/"+n, 0, 0)
			if err != nil {
				r.idxErr = err
				return
			}
			plain, err := r.openFile(raw)
			if err != nil {
				r.idxErr = err
				return
			}
			var f indexFile
			if err := json.Unmarshal(plain, &f); err != nil {
				r.idxErr = err
				return
			}
			for _, p := range f.Packs {
				for _, b := range p.Blobs {
					idx[b.ID] = blobRef{pack: p.ID, offset: b.Offset, length: b.Length, uncompressed: b.Uncompressed}
				}
			}
		}
		r.index = idx
	})
	return r.idxErr
}

// Blob reads one blob (part of a file, or a folder listing).
func (r *Repo) Blob(ctx context.Context, id string) ([]byte, error) {
	if err := r.loadIndex(ctx); err != nil {
		return nil, err
	}
	ref, ok := r.index[id]
	if !ok {
		return nil, fmt.Errorf("blob %.8s is missing from the backup", id)
	}
	if !isID(ref.pack) {
		return nil, errors.New("bad pack id in index")
	}
	raw, err := r.get(ctx, path.Join("data", ref.pack), int64(ref.offset), int64(ref.length))
	if err != nil {
		return nil, err
	}
	plain, err := r.key.open(raw)
	if err != nil {
		return nil, err
	}
	if ref.uncompressed > 0 {
		return zdec.DecodeAll(plain, make([]byte, 0, ref.uncompressed))
	}
	return plain, nil
}

package resticread

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// Node is a file, folder or link in a backup.
type Node struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"` // file, dir, symlink, ...
	Mode       uint32    `json:"mode,omitempty"`
	ModTime    time.Time `json:"mtime"`
	Size       uint64    `json:"size,omitempty"`
	Content    []string  `json:"content,omitempty"`
	Subtree    string    `json:"subtree,omitempty"`
	LinkTarget string    `json:"linktarget,omitempty"`
}

// Tree is the content of one folder.
type Tree struct {
	Nodes []Node `json:"nodes"`
}

// winPath matches a Windows path like C:\Users\anna.
func winPath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// TreePath is where an original path sits inside the backup: restic keeps
// absolute paths as they are (C:\Users becomes /C/Users) and relative ones
// under their last folder's name.
func TreePath(orig string) string {
	switch {
	case winPath(orig):
		return path.Clean("/" + orig[:1] + "/" + strings.ReplaceAll(orig[2:], "\\", "/"))
	case strings.HasPrefix(orig, "/"):
		return path.Clean(orig)
	default:
		return "/" + path.Base(strings.ReplaceAll(orig, "\\", "/"))
	}
}

// Original maps a path inside the backup ("/home/anna/Documents/x.txt",
// "/C/Users/anna/x.txt") to where it was on the PC.
func (s *Snapshot) Original(p string) string {
	p = path.Clean("/" + p)
	for _, sp := range s.Paths {
		tp := TreePath(sp)
		if p != tp && !strings.HasPrefix(p, tp+"/") {
			continue
		}
		rest := strings.TrimPrefix(p, tp)
		if winPath(sp) {
			return strings.TrimRight(sp, "\\/") + strings.ReplaceAll(rest, "/", "\\")
		}
		return strings.TrimRight(sp, "/") + rest
	}
	return p
}

// Root is the deepest folder in the backup that holds all its paths:
// where browsing should start.
func (s *Snapshot) Root() string {
	if len(s.Paths) == 0 {
		return "/"
	}
	common := TreePath(s.Paths[0])
	if len(s.Paths) == 1 {
		// Show the backed-up folder's parent, so its name is visible.
		return path.Dir(common)
	}
	for _, sp := range s.Paths[1:] {
		tp := TreePath(sp)
		for common != "/" && tp != common && !strings.HasPrefix(tp, common+"/") {
			common = path.Dir(common)
		}
	}
	return common
}

// Tree loads a folder listing, remembering it for next time.
func (r *Repo) Tree(ctx context.Context, id string) (*Tree, error) {
	r.cacheMu.Lock()
	t, ok := r.trees[id]
	r.cacheMu.Unlock()
	if ok {
		return t, nil
	}
	b, err := r.Blob(ctx, id)
	if err != nil {
		return nil, err
	}
	t = &Tree{}
	if err := json.Unmarshal(b, t); err != nil {
		return nil, err
	}
	r.cacheMu.Lock()
	if len(r.trees) > 5000 {
		r.trees = map[string]*Tree{}
	}
	r.trees[id] = t
	r.cacheMu.Unlock()
	return t, nil
}

// Find returns the node at an absolute path inside a snapshot ("/" is the
// snapshot's top folder).
func (r *Repo) Find(ctx context.Context, snap *Snapshot, p string) (*Node, error) {
	root := &Node{Name: "/", Type: "dir", Subtree: snap.Tree, ModTime: snap.Time}
	parts := strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/")
	cur := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		if cur.Type != "dir" {
			return nil, fmt.Errorf("%s is not a folder", cur.Name)
		}
		t, err := r.Tree(ctx, cur.Subtree)
		if err != nil {
			return nil, err
		}
		var next *Node
		for i := range t.Nodes {
			if t.Nodes[i].Name == part {
				next = &t.Nodes[i]
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("%s isn't in this backup", p)
		}
		cur = next
	}
	return cur, nil
}

// List returns the entries of a folder in a snapshot, folders first.
func (r *Repo) List(ctx context.Context, snap *Snapshot, dir string) ([]Node, error) {
	n, err := r.Find(ctx, snap, dir)
	if err != nil {
		return nil, err
	}
	if n.Type != "dir" {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	t, err := r.Tree(ctx, n.Subtree)
	if err != nil {
		return nil, err
	}
	out := append([]Node{}, t.Nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Type == "dir") != (out[j].Type == "dir") {
			return out[i].Type == "dir"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	for i := range out {
		out[i].Content = nil // not needed by callers, and can be large
	}
	return out, nil
}

// ErrStop ends a walk early without an error.
var ErrStop = errors.New("stop")

// Walk calls fn for every node under the folder at dir (not dir itself),
// with each node's absolute path.
func (r *Repo) Walk(ctx context.Context, snap *Snapshot, dir string, fn func(p string, n *Node) error) error {
	n, err := r.Find(ctx, snap, dir)
	if err != nil {
		return err
	}
	if n.Type != "dir" {
		return fn(path.Clean("/"+dir), n)
	}
	err = r.walk(ctx, path.Clean("/"+dir), n.Subtree, fn)
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

func (r *Repo) walk(ctx context.Context, dir, tree string, fn func(string, *Node) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t, err := r.Tree(ctx, tree)
	if err != nil {
		return err
	}
	for i := range t.Nodes {
		n := &t.Nodes[i]
		p := path.Join(dir, n.Name)
		if err := fn(p, n); err != nil {
			return err
		}
		if n.Type == "dir" && n.Subtree != "" {
			if err := r.walk(ctx, p, n.Subtree, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteFile streams a file's content to w.
func (r *Repo) WriteFile(ctx context.Context, n *Node, w io.Writer) error {
	if n.Type != "file" {
		return fmt.Errorf("%s is not a file", n.Name)
	}
	for _, id := range n.Content {
		b, err := r.Blob(ctx, id)
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// Size adds up the sizes of the files under the given paths, stopping
// after limit folders have been read (0 for no limit). done is false if it
// stopped early.
func (r *Repo) Size(ctx context.Context, snap *Snapshot, paths []string, limit int) (total uint64, files int, done bool, err error) {
	trees := 0
	for _, p := range paths {
		n, err := r.Find(ctx, snap, p)
		if err != nil {
			return 0, 0, false, err
		}
		if n.Type == "file" {
			total += n.Size
			files++
			continue
		}
		err = r.Walk(ctx, snap, p, func(_ string, n *Node) error {
			switch n.Type {
			case "file":
				total += n.Size
				files++
			case "dir":
				trees++
				if limit > 0 && trees > limit {
					return ErrStop
				}
			}
			return nil
		})
		if err != nil {
			return 0, 0, false, err
		}
		if limit > 0 && trees > limit {
			return total, files, false, nil
		}
	}
	return total, files, true, nil
}

// Search finds files and folders whose name contains query (any case)
// under dir, up to max results.
func (r *Repo) Search(ctx context.Context, snap *Snapshot, dir, query string, max int) ([]string, error) {
	q := strings.ToLower(query)
	var out []string
	err := r.Walk(ctx, snap, dir, func(p string, n *Node) error {
		if strings.Contains(strings.ToLower(n.Name), q) {
			out = append(out, p)
			if len(out) >= max {
				return ErrStop
			}
		}
		return nil
	})
	return out, err
}

// WriteZip writes the given paths (files or folders) of a snapshot as a
// zip archive. Names inside the zip are relative to base. Files are stored
// as they are, without compressing again.
func (r *Repo) WriteZip(ctx context.Context, snap *Snapshot, base string, paths []string, w io.Writer, progress func(bytes uint64)) error {
	zw := zip.NewWriter(w)
	var done uint64
	add := func(p string, n *Node) error {
		rel := strings.TrimPrefix(strings.TrimPrefix(p, path.Clean("/"+base)), "/")
		if rel == "" {
			rel = n.Name
		}
		h := &zip.FileHeader{Name: rel, Modified: n.ModTime, Method: zip.Store}
		switch n.Type {
		case "dir":
			h.Name += "/"
			_, err := zw.CreateHeader(h)
			return err
		case "file":
			h.UncompressedSize64 = n.Size
			fw, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			cw := &countWriter{w: fw, progress: func(k uint64) {
				if progress != nil {
					progress(done + k)
				}
			}}
			if err := r.WriteFile(ctx, n, cw); err != nil {
				return err
			}
			done += cw.n
			return nil
		}
		return nil // links and special files are left out
	}
	for _, p := range paths {
		n, err := r.Find(ctx, snap, p)
		if err != nil {
			return err
		}
		if err := add(p, n); err != nil {
			return err
		}
		if n.Type == "dir" {
			if err := r.Walk(ctx, snap, p, add); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

type countWriter struct {
	w        io.Writer
	n        uint64
	progress func(uint64)
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += uint64(n)
	c.progress(c.n)
	return n, err
}

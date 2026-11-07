//go:build js && wasm

// restash-wasm is the restic reader for the browser. The web UI loads it to
// browse and download backups: it fetches encrypted data from the server
// and decrypts it in the page, so the server never sees file contents or
// names. It exposes one global, restashRestic, whose functions return
// promises.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sync"
	"syscall/js"

	"github.com/mcbulazs/restash/internal/resticread"
)

var (
	mu     sync.Mutex
	repos  = map[int]*resticread.Repo{}
	snaps  = map[int]map[string]*resticread.Snapshot{}
	nextID = 1
)

// promise runs fn in a goroutine (so network calls don't block the page)
// and settles a JS promise with its result.
func promise(fn func() (any, error)) js.Value {
	var handler js.Func
	handler = js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			defer handler.Release()
			defer func() {
				if r := recover(); r != nil {
					reject.Invoke(js.Global().Get("Error").New(fmt.Sprint(r)))
				}
			}()
			v, err := fn()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(handler)
}

func toJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func strings(v js.Value) []string {
	out := make([]string, v.Length())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	return out
}

func lookup(h int, snapID string) (*resticread.Repo, *resticread.Snapshot, error) {
	mu.Lock()
	defer mu.Unlock()
	r := repos[h]
	if r == nil {
		return nil, nil, errors.New("backup not open")
	}
	s := snaps[h][snapID]
	if s == nil && snapID != "" {
		return nil, nil, errors.New("no such snapshot")
	}
	return r, s, nil
}

// await waits for a JS promise from Go.
func await(p js.Value) (js.Value, error) {
	ch := make(chan js.Value, 1)
	errc := make(chan error, 1)
	var ok, fail js.Func
	ok = js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- v
		return nil
	})
	fail = js.FuncOf(func(this js.Value, args []js.Value) any {
		msg := "write failed"
		if len(args) > 0 && args[0].Truthy() {
			msg = args[0].Call("toString").String()
		}
		errc <- errors.New(msg)
		return nil
	})
	defer ok.Release()
	defer fail.Release()
	p.Call("then", ok, fail)
	select {
	case v := <-ch:
		return v, nil
	case err := <-errc:
		return js.Undefined(), err
	}
}

// jsWriter passes data to a JS function write(Uint8Array) => Promise, in
// chunks, waiting for each so a slow disk slows the download instead of
// filling memory.
type jsWriter struct {
	write js.Value
	buf   []byte
}

const chunk = 1 << 20

func (w *jsWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := chunk - len(w.buf)
		take := min(room, len(p))
		w.buf = append(w.buf, p[:take]...)
		p = p[take:]
		if len(w.buf) == chunk {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (w *jsWriter) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	arr := js.Global().Get("Uint8Array").New(len(w.buf))
	js.CopyBytesToJS(arr, w.buf)
	w.buf = w.buf[:0]
	_, err := await(w.write.Invoke(arr))
	return err
}

var _ io.Writer = (*jsWriter)(nil)

func main() {
	ctx := context.Background()
	api := map[string]any{
		// open(repoURL, password) => handle
		"open": js.FuncOf(func(this js.Value, args []js.Value) any {
			url, password := args[0].String(), args[1].String()
			return promise(func() (any, error) {
				r, err := resticread.Open(ctx, url, password, resticread.Options{})
				if err != nil {
					return nil, err
				}
				mu.Lock()
				h := nextID
				nextID++
				repos[h] = r
				snaps[h] = map[string]*resticread.Snapshot{}
				mu.Unlock()
				return h, nil
			})
		}),
		// snapshots(h) => JSON [{id, time, paths, tags, root}]
		"snapshots": js.FuncOf(func(this js.Value, args []js.Value) any {
			h := args[0].Int()
			return promise(func() (any, error) {
				r, _, err := lookup(h, "")
				if err != nil {
					return nil, err
				}
				list, err := r.Snapshots(ctx)
				if err != nil {
					return nil, err
				}
				type out struct {
					resticread.Snapshot
					Root string `json:"root"`
				}
				res := make([]out, 0, len(list))
				mu.Lock()
				for i := range list {
					s := list[i]
					snaps[h][s.ID] = &s
					res = append(res, out{Snapshot: s, Root: s.Root()})
				}
				mu.Unlock()
				return toJSON(res)
			})
		}),
		// list(h, snap, dir) => JSON [{name, type, size, mtime, path, original}]
		"list": js.FuncOf(func(this js.Value, args []js.Value) any {
			h, snapID, dir := args[0].Int(), args[1].String(), args[2].String()
			return promise(func() (any, error) {
				r, s, err := lookup(h, snapID)
				if err != nil {
					return nil, err
				}
				nodes, err := r.List(ctx, s, dir)
				if err != nil {
					return nil, err
				}
				type out struct {
					resticread.Node
					Path     string `json:"path"`
					Original string `json:"original"`
				}
				res := make([]out, 0, len(nodes))
				for _, n := range nodes {
					p := path.Join("/", dir, n.Name)
					res = append(res, out{Node: n, Path: p, Original: s.Original(p)})
				}
				return toJSON(res)
			})
		}),
		// original(h, snap, path) => where it was on the PC
		"original": js.FuncOf(func(this js.Value, args []js.Value) any {
			_, s, err := lookup(args[0].Int(), args[1].String())
			if err != nil {
				return args[2].String()
			}
			return s.Original(args[2].String())
		}),
		// size(h, snap, paths, limit) => JSON {bytes, files, done}
		"size": js.FuncOf(func(this js.Value, args []js.Value) any {
			h, snapID, paths, limit := args[0].Int(), args[1].String(), strings(args[2]), args[3].Int()
			return promise(func() (any, error) {
				r, s, err := lookup(h, snapID)
				if err != nil {
					return nil, err
				}
				total, files, done, err := r.Size(ctx, s, paths, limit)
				if err != nil {
					return nil, err
				}
				return toJSON(map[string]any{"bytes": total, "files": files, "done": done})
			})
		}),
		// search(h, snap, dir, query, max) => JSON [path]
		"search": js.FuncOf(func(this js.Value, args []js.Value) any {
			h, snapID, dir, q, max := args[0].Int(), args[1].String(), args[2].String(), args[3].String(), args[4].Int()
			return promise(func() (any, error) {
				r, s, err := lookup(h, snapID)
				if err != nil {
					return nil, err
				}
				found, err := r.Search(ctx, s, dir, q, max)
				if err != nil {
					return nil, err
				}
				return toJSON(found)
			})
		}),
		// file(h, snap, path, write) => streams one file's content
		"file": js.FuncOf(func(this js.Value, args []js.Value) any {
			h, snapID, p, write := args[0].Int(), args[1].String(), args[2].String(), args[3]
			return promise(func() (any, error) {
				r, s, err := lookup(h, snapID)
				if err != nil {
					return nil, err
				}
				n, err := r.Find(ctx, s, p)
				if err != nil {
					return nil, err
				}
				w := &jsWriter{write: write}
				if err := r.WriteFile(ctx, n, w); err != nil {
					return nil, err
				}
				return nil, w.flush()
			})
		}),
		// zip(h, snap, base, paths, write, progress) => streams a zip
		"zip": js.FuncOf(func(this js.Value, args []js.Value) any {
			h, snapID, base, paths, write, progress := args[0].Int(), args[1].String(), args[2].String(), strings(args[3]), args[4], args[5]
			return promise(func() (any, error) {
				r, s, err := lookup(h, snapID)
				if err != nil {
					return nil, err
				}
				w := &jsWriter{write: write}
				err = r.WriteZip(ctx, s, base, paths, w, func(n uint64) {
					if progress.Truthy() {
						progress.Invoke(float64(n))
					}
				})
				if err != nil {
					return nil, err
				}
				return nil, w.flush()
			})
		}),
		// close(h) forgets an opened backup.
		"close": js.FuncOf(func(this js.Value, args []js.Value) any {
			mu.Lock()
			delete(repos, args[0].Int())
			delete(snaps, args[0].Int())
			mu.Unlock()
			return nil
		}),
	}
	js.Global().Set("restashRestic", js.ValueOf(api))
	select {} // keep running
}

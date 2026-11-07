package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mcbulazs/restash/internal/proto"
)

// LinkHandler gets what the server sends. Any field may be nil.
type LinkHandler struct {
	OnUp      func()                            // linked (again)
	OnEntries func()                            // entries changed
	OnRun     func(entry string)                // back up now ("" for all)
	OnBrowse  func(data string) (string, error) // folder listing request
	OnRestore func(id, data string, send func(proto.Message))
}

// RunLink keeps a websocket open to the server, reconnecting with backoff,
// until ctx is cancelled or the server says the PC is unlinked.
func RunLink(ctx context.Context, cfg *Config, version string, h LinkHandler) error {
	wait := time.Second
	for {
		start := time.Now()
		err := linkOnce(ctx, cfg, version, h)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrUnlinked) {
			return err
		}
		if time.Since(start) > time.Minute {
			wait = time.Second // it was up for a while; start over
		}
		slog.Warn("lost the link to the server; retrying", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func wsURL(server string) string {
	if rest, ok := strings.CutPrefix(server, "https://"); ok {
		return "wss://" + rest
	}
	return "ws://" + strings.TrimPrefix(server, "http://")
}

func linkOnce(ctx context.Context, cfg *Config, version string, h LinkHandler) error {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, wsURL(strings.TrimSuffix(cfg.Server, "/"))+"/api/agent/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.PCID + "." + cfg.Token}},
	})
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return ErrUnlinked
	}
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	var writeMu sync.Mutex // answers are written from several goroutines

	if err := wsjson.Write(ctx, c, proto.Message{Type: proto.TypeHello, Version: version, Timezone: Timezone()}); err != nil {
		return err
	}
	if h.OnUp != nil {
		h.OnUp()
	}
	for {
		var msg proto.Message
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			return err
		}
		switch msg.Type {
		case proto.TypeEntries:
			if h.OnEntries != nil {
				h.OnEntries()
			}
		case proto.TypeRun:
			if h.OnRun != nil {
				h.OnRun(msg.Entry)
			}
		case proto.TypeRestore:
			if h.OnRestore == nil {
				continue
			}
			send := func(m proto.Message) {
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				writeMu.Lock()
				defer writeMu.Unlock()
				wsjson.Write(wctx, c, m)
			}
			go h.OnRestore(msg.ID, msg.Data, send)
		case proto.TypeBrowse:
			if h.OnBrowse == nil {
				continue
			}
			// Answer in the background so slow disks don't hold up the link.
			go func(msg proto.Message) {
				data, err := h.OnBrowse(msg.Data)
				if err != nil {
					slog.Warn("folder listing request refused", "err", err)
					return
				}
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				writeMu.Lock()
				defer writeMu.Unlock()
				wsjson.Write(wctx, c, proto.Message{Type: proto.TypeBrowseResult, ID: msg.ID, Data: data})
			}(msg)
		}
	}
}

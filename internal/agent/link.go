package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mcbulazs/restash/internal/proto"
)

// RunLink keeps a websocket open to the server, reconnecting with backoff,
// until ctx is cancelled or the server says the PC is unlinked.
func RunLink(ctx context.Context, cfg *Config, version string, onUp func()) error {
	wait := time.Second
	for {
		start := time.Now()
		err := linkOnce(ctx, cfg, version, onUp)
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

func linkOnce(ctx context.Context, cfg *Config, version string, onUp func()) error {
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
	c.SetReadLimit(64 * 1024)

	if err := wsjson.Write(ctx, c, proto.Message{Type: proto.TypeHello, Version: version}); err != nil {
		return err
	}
	if onUp != nil {
		onUp()
	}
	for {
		var msg proto.Message
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			return err
		}
		// No server commands yet; schedules and restores will arrive here.
		slog.Debug("message from server", "type", msg.Type)
	}
}

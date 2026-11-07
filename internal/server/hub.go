package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mcbulazs/restash/internal/proto"
)

// errOffline means the PC isn't connected right now.
var errOffline = errors.New("this PC is offline right now")

type agentConn struct {
	c  *websocket.Conn
	mu sync.Mutex // one writer at a time
}

func (a *agentConn) send(ctx context.Context, msg proto.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, a.c, msg)
}

// pending is a request sent to a PC that waits for its answer.
type pending struct {
	pcID string
	ch   chan string
}

// hub tracks which agents are connected and carries messages to them.
type hub struct {
	mu      sync.Mutex
	conns   map[string]*agentConn
	waiting map[string]pending
}

func newHub() *hub {
	return &hub{conns: map[string]*agentConn{}, waiting: map[string]pending{}}
}

func (h *hub) add(id string, c *websocket.Conn) *agentConn {
	conn := &agentConn{c: c}
	h.mu.Lock()
	old := h.conns[id]
	h.conns[id] = conn
	h.mu.Unlock()
	if old != nil {
		old.c.Close(websocket.StatusPolicyViolation, "connected from somewhere else")
	}
	return conn
}

func (h *hub) remove(id string, c *agentConn) {
	h.mu.Lock()
	if h.conns[id] == c {
		delete(h.conns, id)
	}
	h.mu.Unlock()
}

func (h *hub) online(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[id] != nil
}

// send delivers a message to a PC if it is connected.
func (h *hub) send(ctx context.Context, pcID string, msg proto.Message) error {
	h.mu.Lock()
	c := h.conns[pcID]
	h.mu.Unlock()
	if c == nil {
		return errOffline
	}
	return c.send(ctx, msg)
}

// ask sends a request to a PC and waits for its answer.
func (h *hub) ask(ctx context.Context, pcID string, msg proto.Message) (string, error) {
	b := make([]byte, 12)
	rand.Read(b)
	msg.ID = hex.EncodeToString(b)
	ch := make(chan string, 1)
	h.mu.Lock()
	h.waiting[msg.ID] = pending{pcID: pcID, ch: ch}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waiting, msg.ID)
		h.mu.Unlock()
	}()
	if err := h.send(ctx, pcID, msg); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case data := <-ch:
		return data, nil
	case <-ctx.Done():
		return "", errors.New("the PC didn't answer in time")
	}
}

// deliver hands a PC's answer to whoever asked. Answers from another PC are
// ignored.
func (h *hub) deliver(pcID, id, data string) {
	h.mu.Lock()
	p, ok := h.waiting[id]
	h.mu.Unlock()
	if ok && p.pcID == pcID {
		select {
		case p.ch <- data:
		default:
		}
	}
}

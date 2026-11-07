package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/store"
)

func pcFrom(ctx context.Context) *store.PC { return ctx.Value(ctxPC).(*store.PC) }

// agentCreds reads "Authorization: Bearer <pc id>.<token>".
func agentCreds(r *http.Request) (id, token string, ok bool) {
	h := r.Header.Get("Authorization")
	rest, found := strings.CutPrefix(h, "Bearer ")
	if !found {
		return "", "", false
	}
	return strings.Cut(rest, ".")
}

// agent requires a joined PC's token.
func (s *Server) agent(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, token, ok := agentCreds(r)
		if !ok {
			httpError(w, http.StatusUnauthorized, "missing PC token")
			return
		}
		pc, err := s.store.AuthPC(r.Context(), id, token)
		if isNotFound(err) {
			httpError(w, http.StatusUnauthorized, "this PC is not linked to restash anymore")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxPC, pc)))
	})
}

func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	for utf8.RuneCountInString(s) > max {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func (s *Server) handleAgentJoin(w http.ResponseWriter, r *http.Request) {
	var req proto.JoinRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Codes are 30 bits and live an hour; this keeps guessing hopeless.
	if !s.limits.allow("join", 30, time.Minute) {
		httpError(w, http.StatusTooManyRequests, "too many tries; wait a minute")
		return
	}
	p := store.PC{
		Name:         clean(req.Name, 60),
		OS:           clean(req.OS, 40),
		Arch:         clean(req.Arch, 20),
		AgentVersion: clean(req.Version, 40),
	}
	if p.Name == "" {
		p.Name = "PC"
	}
	pc, token, err := s.store.Join(r.Context(), req.Code, p)
	if isNotFound(err) {
		httpError(w, http.StatusNotFound, "that code is wrong, used or expired; get a new one on the website")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	u, err := s.store.UserByID(r.Context(), pc.UserID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	slog.Info("PC joined", "pc", pc.ID, "user", u.ID, "os", pc.OS)
	writeJSON(w, http.StatusOK, proto.JoinResponse{PCID: pc.ID, Token: token, Email: u.Email, Name: u.Name})
}

func (s *Server) handleAgentAccount(w http.ResponseWriter, r *http.Request) {
	pc := pcFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), pc.UserID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, proto.Account{
		Email: u.Email, Name: u.Name,
		KDFSalt: u.KDFSalt, KDFIterations: u.KDFIterations,
		AccountKey: u.AccountKey, PCKey: pc.PCKey,
	})
}

func (s *Server) handleAgentSetKey(w http.ResponseWriter, r *http.Request) {
	var req proto.SetKey
	if !readJSON(w, r, &req) {
		return
	}
	if !validSealedKey(req.PCKey) {
		httpError(w, http.StatusBadRequest, "bad key")
		return
	}
	switch err := s.store.SetPCKey(r.Context(), pcFrom(r.Context()).ID, req.PCKey); {
	case err == store.ErrKeySet:
		httpError(w, http.StatusConflict, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleAgentBackup(w http.ResponseWriter, r *http.Request) {
	var req proto.BackupReport
	if !readJSON(w, r, &req) {
		return
	}
	if req.FinishedAt.IsZero() || req.StartedAt.After(req.FinishedAt) || req.FinishedAt.After(s.now().Add(time.Hour)) {
		httpError(w, http.StatusBadRequest, "bad times")
		return
	}
	b := store.Backup{
		PCID: pcFrom(r.Context()).ID, Entry: clean(req.Entry, 64),
		StartedAt: req.StartedAt, FinishedAt: req.FinishedAt, OK: req.OK,
		Files: req.Files, Bytes: req.Bytes, Added: req.Added, Snapshot: clean(req.Snapshot, 64),
	}
	if req.Detail != "" {
		if len(req.Detail) > 16*1024 || !validSealed(req.Detail) {
			httpError(w, http.StatusBadRequest, "bad detail")
			return
		}
		b.Detail = req.Detail
	}
	if err := s.store.AddBackup(r.Context(), b); err != nil {
		internalError(w, r, err)
		return
	}
	s.sizes.forget(b.PCID)
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentWS keeps a link open to the agent, so the server knows it is
// online and can send it commands.
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	pc := pcFrom(r.Context())
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20) // folder listings can be big
	conn := s.hub.add(pc.ID, c)
	s.store.TouchPC(context.Background(), pc.ID)
	defer func() {
		s.hub.remove(pc.ID, conn)
		s.store.TouchPC(context.Background(), pc.ID)
		c.CloseNow()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		// Pings keep the link alive through proxies and catch dead PCs.
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
				err := c.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					c.CloseNow()
					return
				}
				s.store.TouchPC(context.Background(), pc.ID)
			}
		}
	}()

	for {
		var msg proto.Message
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			return
		}
		switch msg.Type {
		case proto.TypeHello:
			tz := clean(msg.Timezone, 64)
			if _, err := time.LoadLocation(tz); err != nil {
				tz = ""
			}
			s.store.HelloPC(context.Background(), pc.ID, clean(msg.Version, 40), tz)
		case proto.TypeBrowseResult:
			s.hub.deliver(pc.ID, msg.ID, msg.Data)
		}
	}
}

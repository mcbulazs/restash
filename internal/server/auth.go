package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mcbulazs/restash/internal/store"
	"github.com/mcbulazs/restash/internal/vault"
)

const sessionCookie = "restash_session"

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxPC
)

func userFrom(ctx context.Context) *store.User { return ctx.Value(ctxUser).(*store.User) }

// user requires a logged-in browser session.
func (s *Server) user(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			httpError(w, http.StatusUnauthorized, "please log in")
			return
		}
		u, err := s.store.SessionUser(r.Context(), c.Value)
		if isNotFound(err) {
			httpError(w, http.StatusUnauthorized, "please log in")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
	})
}

// admin requires a logged-in admin.
func (s *Server) admin(next http.HandlerFunc) http.Handler {
	return s.user(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).Admin {
			httpError(w, http.StatusForbidden, "only the admin can do this")
			return
		}
		next(w, r)
	})
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(store.SessionLength / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.PublicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

type userJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

func toUserJSON(u *store.User) userJSON {
	return userJSON{ID: u.ID, Email: u.Email, Name: u.Name, Admin: u.Admin}
}

type paramsJSON struct {
	KDFSalt       string `json:"kdfSalt"`
	KDFIterations int    `json:"kdfIterations"`
}

// handleParams gives the browser what it needs to turn a password into keys.
// Unknown emails get a stable made-up salt, so this doesn't reveal who has
// an account.
func (s *Server) handleParams(w http.ResponseWriter, r *http.Request) {
	email := store.NormalizeEmail(r.URL.Query().Get("email"))
	u, err := s.store.UserByEmail(r.Context(), email)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, paramsJSON{u.KDFSalt, u.KDFIterations})
	case isNotFound(err):
		mac := hmac.New(sha256.New, s.fakeSalt)
		mac.Write([]byte(email))
		writeJSON(w, http.StatusOK, paramsJSON{base64.StdEncoding.EncodeToString(mac.Sum(nil)[:16]), vault.Iterations})
	default:
		internalError(w, r, err)
	}
}

type loginRequest struct {
	Email string `json:"email"`
	Auth  string `json:"auth"`
}

type loginResponse struct {
	User       userJSON `json:"user"`
	AccountKey string   `json:"accountKey"`
}

func decodeKey(s string) ([]byte, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	return b, err == nil && len(b) == 32
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !readJSON(w, r, &req) {
		return
	}
	email := store.NormalizeEmail(req.Email)
	if !s.limits.allow("login:"+email, 10, 15*time.Minute) {
		httpError(w, http.StatusTooManyRequests, "too many tries; wait a few minutes")
		return
	}
	auth, ok := decodeKey(req.Auth)
	u, err := s.store.UserByEmail(r.Context(), email)
	if err != nil && !isNotFound(err) {
		internalError(w, r, err)
		return
	}
	if !ok || u == nil || !vault.CheckAuth(auth, u.AuthHash) {
		httpError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	tok, err := s.store.CreateSession(r.Context(), u.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	s.setSession(w, tok)
	writeJSON(w, http.StatusOK, loginResponse{User: toUserJSON(u), AccountKey: u.AccountKey})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	writeJSON(w, http.StatusOK, struct {
		User          userJSON `json:"user"`
		AccountKey    string   `json:"accountKey"`
		KDFSalt       string   `json:"kdfSalt"`
		KDFIterations int      `json:"kdfIterations"`
	}{toUserJSON(u), u.AccountKey, u.KDFSalt, u.KDFIterations})
}

func (s *Server) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.InviteByToken(r.Context(), r.PathValue("token"))
	if isNotFound(err) {
		httpError(w, http.StatusNotFound, store.ErrInviteInvalid.Error())
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Email     string    `json:"email"`
		Admin     bool      `json:"admin"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{inv.Email, inv.Admin, inv.ExpiresAt})
}

// newCredentials are the password-dependent fields a browser sends.
type newCredentials struct {
	KDFSalt       string `json:"kdfSalt"`
	KDFIterations int    `json:"kdfIterations"`
	Auth          string `json:"auth"`
	AccountKey    string `json:"accountKey"`
}

// Sealed 32-byte keys are 12 (nonce) + 32 + 16 (tag) bytes.
func validSealedKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 60
}

func (c newCredentials) check() (store.Credentials, error) {
	salt, err := base64.StdEncoding.DecodeString(c.KDFSalt)
	if err != nil || len(salt) != 16 {
		return store.Credentials{}, errors.New("bad salt")
	}
	if c.KDFIterations < vault.Iterations || c.KDFIterations > 10*vault.Iterations {
		return store.Credentials{}, errors.New("bad iteration count")
	}
	auth, ok := decodeKey(c.Auth)
	if !ok || !validSealedKey(c.AccountKey) {
		return store.Credentials{}, errors.New("bad keys")
	}
	return store.Credentials{KDFSalt: c.KDFSalt, KDFIterations: c.KDFIterations, AuthHash: vault.HashAuth(auth), AccountKey: c.AccountKey}, nil
}

type signupRequest struct {
	Invite string `json:"invite"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	newCredentials
	RecoveryAuth       string `json:"recoveryAuth"`
	RecoveryAccountKey string `json:"recoveryAccountKey"`
}

func validEmail(e string) bool {
	at := strings.IndexByte(e, '@')
	return at > 0 && at < len(e)-1 && len(e) <= 254 && !strings.ContainsAny(e, " \t\r\n<>")
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !s.limits.allow("signup", 20, time.Hour) {
		httpError(w, http.StatusTooManyRequests, "too many tries; wait a while")
		return
	}
	email := store.NormalizeEmail(req.Email)
	name := strings.TrimSpace(req.Name)
	if !validEmail(email) {
		httpError(w, http.StatusBadRequest, "that doesn't look like an email address")
		return
	}
	if name == "" || utf8.RuneCountInString(name) > 60 {
		httpError(w, http.StatusBadRequest, "please give a name of up to 60 characters")
		return
	}
	creds, err := req.newCredentials.check()
	recAuth, ok := decodeKey(req.RecoveryAuth)
	if err != nil || !ok || !validSealedKey(req.RecoveryAccountKey) {
		httpError(w, http.StatusBadRequest, "bad keys")
		return
	}
	u := &store.User{
		Email: email, Name: name,
		KDFSalt: creds.KDFSalt, KDFIterations: creds.KDFIterations, AuthHash: creds.AuthHash, AccountKey: creds.AccountKey,
		RecoveryAuthHash: vault.HashAuth(recAuth), RecoveryAccountKey: req.RecoveryAccountKey,
	}
	switch err := s.store.CreateUser(r.Context(), req.Invite, u); {
	case errors.Is(err, store.ErrInviteInvalid), errors.Is(err, store.ErrInviteEmail), errors.Is(err, store.ErrEmailTaken):
		httpError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		internalError(w, r, err)
		return
	}
	s.startSession(w, r, u)
}

type recoverKeyRequest struct {
	Email        string `json:"email"`
	RecoveryAuth string `json:"recoveryAuth"`
}

// recoveryUser checks a recovery code's auth key.
func (s *Server) recoveryUser(w http.ResponseWriter, r *http.Request, email, recoveryAuth string) *store.User {
	email = store.NormalizeEmail(email)
	if !s.limits.allow("recover:"+email, 10, time.Hour) {
		httpError(w, http.StatusTooManyRequests, "too many tries; wait a while")
		return nil
	}
	auth, ok := decodeKey(recoveryAuth)
	u, err := s.store.UserByEmail(r.Context(), email)
	if err != nil && !isNotFound(err) {
		internalError(w, r, err)
		return nil
	}
	if !ok || u == nil || !vault.CheckAuth(auth, u.RecoveryAuthHash) {
		httpError(w, http.StatusUnauthorized, "wrong email or recovery code")
		return nil
	}
	return u
}

// handleRecoverKey hands out the account key locked with the recovery code,
// so the browser can lock it again with a new password.
func (s *Server) handleRecoverKey(w http.ResponseWriter, r *http.Request) {
	var req recoverKeyRequest
	if !readJSON(w, r, &req) {
		return
	}
	u := s.recoveryUser(w, r, req.Email, req.RecoveryAuth)
	if u == nil {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		RecoveryAccountKey string `json:"recoveryAccountKey"`
	}{u.RecoveryAccountKey})
}

type recoverRequest struct {
	recoverKeyRequest
	New newCredentials `json:"new"`
}

func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	var req recoverRequest
	if !readJSON(w, r, &req) {
		return
	}
	u := s.recoveryUser(w, r, req.Email, req.RecoveryAuth)
	if u == nil {
		return
	}
	creds, err := req.New.check()
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ResetCredentials(r.Context(), u.ID, creds); err != nil {
		internalError(w, r, err)
		return
	}
	u.AccountKey = creds.AccountKey
	s.startSession(w, r, u)
}

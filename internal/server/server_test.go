package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/store"
	"github.com/mcbulazs/restash/internal/trash"
	"github.com/mcbulazs/restash/internal/vault"
)

type env struct {
	t     *testing.T
	srv   *httptest.Server
	store *store.Store
	rest  []string // requests that reached rest-server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, store: st}
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.rest = append(e.rest, r.Method+" "+r.URL.Path)
	}))
	t.Cleanup(rest.Close)
	restURL, _ := url.Parse(rest.URL)
	pub, _ := url.Parse("http://restash.test")
	s, err := New(context.Background(), Config{
		PublicURL: pub, Store: st,
		Repos:    trash.NewProxy(restURL, filepath.Join(dir, "repos"), filepath.Join(dir, "trash")),
		ReposDir: filepath.Join(dir, "repos"), Storage: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// call sends a JSON request and decodes the reply into out (if not nil).
func (e *env) call(c *http.Client, method, path string, body, out any, hdr ...string) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

var b64 = base64.StdEncoding.EncodeToString

// account mimics what the browser does at signup.
type account struct {
	email, password, recovery string
	accountKey                []byte
}

func (e *env) signup(c *http.Client, invite, email, password string) account {
	e.t.Helper()
	salt := vault.NewSalt()
	k, _ := vault.PasswordKeys(password, salt, vault.Iterations)
	acct := vault.NewKey()
	code := vault.NewRecoveryCode()
	rk, _ := vault.RecoveryKeys(code)
	locked, _ := vault.Seal(k.Wrap, acct, vault.LabelAccountKey)
	recLocked, _ := vault.Seal(rk.Wrap, acct, vault.LabelAccountKey)
	status := e.call(c, "POST", "/api/auth/signup", map[string]any{
		"invite": invite, "email": email, "name": "Anna",
		"kdfSalt": b64(salt), "kdfIterations": vault.Iterations, "auth": b64(k.Auth), "accountKey": locked,
		"recoveryAuth": b64(rk.Auth), "recoveryAccountKey": recLocked,
	}, nil)
	if status != 200 {
		e.t.Fatalf("signup: %d", status)
	}
	return account{email, password, code, acct}
}

func (e *env) login(c *http.Client, email, password string) int {
	var p paramsJSON
	e.call(c, "GET", "/api/auth/params?email="+url.QueryEscape(email), nil, &p)
	salt, _ := base64.StdEncoding.DecodeString(p.KDFSalt)
	k, _ := vault.PasswordKeys(password, salt, p.KDFIterations)
	return e.call(c, "POST", "/api/auth/login", loginRequest{Email: email, Auth: b64(k.Auth)}, nil)
}

func TestAccountsAndPCs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	setup, _ := e.store.SetupInvite(ctx)

	admin := e.client()
	a := e.signup(admin, setup.Token, "anna@example.com", "hunter2 hunter2")
	var me struct {
		User userJSON `json:"user"`
	}
	if e.call(admin, "GET", "/api/me", nil, &me); !me.User.Admin {
		t.Fatalf("first user is not admin: %+v", me)
	}

	// Logging in, with the right and wrong password.
	other := e.client()
	if s := e.login(other, a.email, "wrong"); s != 401 {
		t.Fatalf("wrong password: %d", s)
	}
	if s := e.login(other, "nobody@example.com", "x"); s != 401 {
		t.Fatalf("unknown user: %d", s)
	}
	if s := e.login(other, "ANNA@example.com", a.password); s != 200 {
		t.Fatalf("login: %d", s)
	}

	// Invites: admin only.
	var inv inviteJSON
	if s := e.call(admin, "POST", "/api/invites", map[string]string{"email": "bob@example.com"}, &inv); s != 200 || !strings.Contains(inv.Link, "/signup?invite=") {
		t.Fatalf("invite: %d %+v", s, inv)
	}
	bob := e.client()
	e.signup(bob, inv.Token, "bob@example.com", "bob's password")
	if s := e.call(bob, "GET", "/api/people", nil, nil); s != 403 {
		t.Fatalf("member saw people: %d", s)
	}
	if s := e.call(bob, "POST", "/api/invites", map[string]string{"email": "x@example.com"}, nil); s != 403 {
		t.Fatalf("member invited: %d", s)
	}

	// Cross-site writes from a browser are refused.
	if s := e.call(admin, "POST", "/api/invites", map[string]string{"email": "x@example.com"}, nil, "Sec-Fetch-Site", "cross-site"); s != 403 {
		t.Fatalf("cross-site write allowed: %d", s)
	}

	// Recovery: reset the password with the recovery code.
	rk, _ := vault.RecoveryKeys(a.recovery)
	var rec struct {
		RecoveryAccountKey string `json:"recoveryAccountKey"`
	}
	if s := e.call(e.client(), "POST", "/api/auth/recover/key", recoverKeyRequest{a.email, b64(rk.Auth)}, &rec); s != 200 {
		t.Fatalf("recover key: %d", s)
	}
	got, err := vault.Open(rk.Wrap, rec.RecoveryAccountKey, vault.LabelAccountKey)
	if err != nil || !bytes.Equal(got, a.accountKey) {
		t.Fatal("recovery didn't give back the account key")
	}
	salt := vault.NewSalt()
	nk, _ := vault.PasswordKeys("new password!", salt, vault.Iterations)
	locked, _ := vault.Seal(nk.Wrap, got, vault.LabelAccountKey)
	if s := e.call(e.client(), "POST", "/api/auth/recover", recoverRequest{recoverKeyRequest{a.email, b64(rk.Auth)},
		newCredentials{b64(salt), vault.Iterations, b64(nk.Auth), locked}}, nil); s != 200 {
		t.Fatalf("recover: %d", s)
	}
	if s := e.call(admin, "GET", "/api/me", nil, nil); s != 401 {
		t.Fatalf("old session survived reset: %d", s)
	}
	if e.login(admin, a.email, "hunter2 hunter2") != 401 || e.login(admin, a.email, "new password!") != 200 {
		t.Fatal("password not changed")
	}

	// Joining a PC.
	var jc joinCodeJSON
	e.call(admin, "POST", "/api/pcs/join", nil, &jc)
	var status map[string]any
	e.call(admin, "GET", "/api/pcs/join/"+jc.Code, nil, &status)
	if status["status"] != "waiting" {
		t.Fatalf("status %v", status)
	}
	if s := e.call(bob, "GET", "/api/pcs/join/"+jc.Code, nil, &status); status["status"] != "expired" {
		t.Fatalf("bob sees anna's code: %d %v", s, status)
	}
	agent := &http.Client{}
	var joined proto.JoinResponse
	if s := e.call(agent, "POST", "/api/agent/join", proto.JoinRequest{Code: strings.ToLower(jc.Code), Name: "laptop", OS: "linux", Arch: "amd64"}, &joined); s != 200 {
		t.Fatalf("join: %d", s)
	}
	if s := e.call(agent, "POST", "/api/agent/join", proto.JoinRequest{Code: jc.Code, Name: "again"}, nil); s != 404 {
		t.Fatalf("code reused: %d", s)
	}
	bearer := []string{"Authorization", "Bearer " + joined.PCID + "." + joined.Token}

	var acc proto.Account
	if s := e.call(agent, "GET", "/api/agent/account", nil, &acc, bearer...); s != 200 || acc.PCKey != "" {
		t.Fatalf("account: %d %+v", s, acc)
	}
	// The agent unlocks the account key with the password, like the browser.
	salt, _ = base64.StdEncoding.DecodeString(acc.KDFSalt)
	k, _ := vault.PasswordKeys("new password!", salt, acc.KDFIterations)
	acctKey, err := vault.Open(k.Wrap, acc.AccountKey, vault.LabelAccountKey)
	if err != nil {
		t.Fatal(err)
	}
	pcKey, _ := vault.Seal(acctKey, vault.NewKey(), vault.LabelPCKey(joined.PCID))
	if s := e.call(agent, "PUT", "/api/agent/key", proto.SetKey{PCKey: pcKey}, nil, bearer...); s != 204 {
		t.Fatalf("set key: %d", s)
	}
	if s := e.call(agent, "PUT", "/api/agent/key", proto.SetKey{PCKey: pcKey}, nil, bearer...); s != 409 {
		t.Fatalf("key replaced: %d", s)
	}
	if s := e.call(agent, "GET", "/api/agent/account", nil, nil, "Authorization", "Bearer "+joined.PCID+".wrong"); s != 401 {
		t.Fatalf("wrong token: %d", s)
	}
	e.call(admin, "GET", "/api/pcs/join/"+jc.Code, nil, &status)
	if status["status"] != "ready" {
		t.Fatalf("status %v", status)
	}

	now := time.Now()
	if s := e.call(agent, "POST", "/api/agent/backups", proto.BackupReport{Entry: "manual", StartedAt: now.Add(-time.Minute), FinishedAt: now, OK: true, Files: 3, Bytes: 100}, nil, bearer...); s != 204 {
		t.Fatalf("report: %d", s)
	}
	var pcs []pcJSON
	e.call(admin, "GET", "/api/pcs", nil, &pcs)
	if len(pcs) != 1 || !pcs[0].Ready || pcs[0].LastOK == nil || len(pcs[0].Recent) != 1 || pcs[0].Name != "laptop" {
		t.Fatalf("pcs %+v", pcs)
	}
	e.call(bob, "GET", "/api/pcs", nil, &pcs)
	if len(pcs) != 0 {
		t.Fatalf("bob sees anna's PCs: %+v", pcs)
	}

	var people peopleJSON
	e.call(admin, "GET", "/api/people", nil, &people)
	if len(people.People) != 2 || people.People[0].PCs != 1 || !people.People[0].AllSafe {
		t.Fatalf("people %+v", people.People)
	}
}

func TestRepoAuth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inv, _ := e.store.SetupInvite(ctx)
	e.signup(e.client(), inv.Token, "a@example.com", "pw")
	users, _ := e.store.Users(ctx)
	code, _, _ := e.store.CreateJoinCode(ctx, users[0].ID)
	pc, token, _ := e.store.Join(ctx, code, store.PC{Name: "x"})
	code2, _, _ := e.store.CreateJoinCode(ctx, users[0].ID)
	pc2, _, _ := e.store.Join(ctx, code2, store.PC{Name: "y"})

	try := func(user, pass, path string) int {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := []struct {
		user, pass, path string
		want             int
	}{
		{pc.ID, token, "/repo/" + pc.ID + "/config", 200},
		{pc.ID, token, "/repo/" + pc.ID + "/", 200},
		{"", "", "/repo/" + pc.ID + "/config", 401},
		{pc.ID, "wrong", "/repo/" + pc.ID + "/config", 401},
		{pc.ID, token, "/repo/" + pc2.ID + "/config", 403},
		{pc.ID, token, "/repo/", 403},
		{pc.ID, token, "/repo/" + pc.ID + "/../" + pc2.ID + "/config", 307}, // redirected to the clean path, then refused
		{pc.ID, token, "/repo/" + pc.ID + "%2F..%2F" + pc2.ID + "/config", 403},
	}
	for _, c := range cases {
		if got := try(c.user, c.pass, c.path); got != c.want {
			t.Errorf("%s as %q: got %d, want %d", c.path, c.user, got, c.want)
		}
	}
	if len(e.rest) != 2 {
		t.Fatalf("rest-server saw %v", e.rest)
	}
}

package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

// vectors are shared with web/src/vault.test.ts.
type vectors struct {
	Password   string `json:"password"`
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Auth       string `json:"auth"`
	Wrap       string `json:"wrap"`
	Recovery   string `json:"recovery"`
	RecAuth    string `json:"recoveryAuth"`
	RecWrap    string `json:"recoveryWrap"`
	SealKey    string `json:"sealKey"`
	SealNonce  string `json:"sealNonce"`
	SealLabel  string `json:"sealLabel"`
	SealPlain  string `json:"sealPlain"`
	Sealed     string `json:"sealed"`
	AuthHash   string `json:"authHash"`
	PCPassword string `json:"pcPassword"`
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func compute(t *testing.T) vectors {
	t.Helper()
	v := vectors{
		Password:   "correct horse battery staple ✓",
		Salt:       b64([]byte("0123456789abcdef")),
		Iterations: 1000,
		Recovery:   "h3vd q8ln 2wkt-m7rc-9bxf-j5pz",
		SealKey:    b64(bytes.Repeat([]byte{7}, 32)),
		SealNonce:  b64(bytes.Repeat([]byte{9}, 12)),
		SealLabel:  LabelPCKey("pc123"),
		SealPlain:  "restic password",
	}
	salt, _ := base64.StdEncoding.DecodeString(v.Salt)
	k, err := PasswordKeys(v.Password, salt, v.Iterations)
	if err != nil {
		t.Fatal(err)
	}
	v.Auth, v.Wrap = b64(k.Auth), b64(k.Wrap)
	rk, err := RecoveryKeys(v.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	v.RecAuth, v.RecWrap = b64(rk.Auth), b64(rk.Wrap)
	key, _ := base64.StdEncoding.DecodeString(v.SealKey)
	nonce, _ := base64.StdEncoding.DecodeString(v.SealNonce)
	v.Sealed, err = sealWithNonce(key, nonce, []byte(v.SealPlain), v.SealLabel)
	if err != nil {
		t.Fatal(err)
	}
	v.AuthHash = HashAuth(k.Auth)
	v.PCPassword = PCPassword(key)
	return v
}

func TestVectors(t *testing.T) {
	got := compute(t)
	const path = "testdata/vectors.json"
	if *update {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want vectors
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("vectors changed:\n got %+v\nwant %+v", got, want)
	}
}

func TestSealOpen(t *testing.T) {
	key := NewKey()
	s, err := Seal(key, []byte("secret"), LabelAccountKey)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := Open(key, s, LabelAccountKey); err != nil || string(p) != "secret" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := Open(key, s, LabelPCKey("x")); err != ErrLocked {
		t.Fatal("opened with wrong label")
	}
	if _, err := Open(NewKey(), s, LabelAccountKey); err != ErrLocked {
		t.Fatal("opened with wrong key")
	}
	if _, err := Open(key, "not base64!", LabelAccountKey); err != ErrLocked {
		t.Fatal("opened garbage")
	}
}

func TestRecoveryCode(t *testing.T) {
	c := NewRecoveryCode()
	if len(c) != 29 || strings.Count(c, "-") != 5 {
		t.Fatalf("code %q", c)
	}
	n, err := NormalizeRecoveryCode(strings.ToLower(strings.ReplaceAll(c, "-", " ")))
	if err != nil || n != strings.ReplaceAll(c, "-", "") {
		t.Fatalf("%q %v", n, err)
	}
	// O/I/L typed for 0/1 still work.
	a, _ := RecoveryKeys("0000-1111-2222-3333-4444-5555")
	b, _ := RecoveryKeys("oooo-iiii-2222-3333-4444-5555")
	if !bytes.Equal(a.Wrap, b.Wrap) {
		t.Fatal("look-alike letters not normalized")
	}
	for _, bad := range []string{"", "ABCD", "UUUU-UUUU-UUUU-UUUU-UUUU-UUUU"} {
		if _, err := NormalizeRecoveryCode(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckAuth(t *testing.T) {
	a := NewKey()
	h := HashAuth(a)
	if !CheckAuth(a, h) || CheckAuth(NewKey(), h) {
		t.Fatal("CheckAuth wrong")
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func newUser(email string) *User {
	return &User{Email: email, Name: "Anna", KDFSalt: "s", KDFIterations: 1, AuthHash: "h", AccountKey: "k", RecoveryAuthHash: "rh", RecoveryAccountKey: "rk"}
}

func TestSetupAndInvites(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	setup, err := s.SetupInvite(ctx)
	if err != nil || !setup.Admin {
		t.Fatalf("%+v %v", setup, err)
	}
	again, _ := s.SetupInvite(ctx)
	if again.Token != setup.Token {
		t.Fatal("setup invite not reused")
	}
	admin := newUser("Anna@Example.com ")
	if err := s.CreateUser(ctx, setup.Token, admin); err != nil {
		t.Fatal(err)
	}
	if !admin.Admin || admin.Email != "anna@example.com" {
		t.Fatalf("%+v", admin)
	}
	if _, err := s.SetupInvite(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("setup invite after first user")
	}
	if err := s.CreateUser(ctx, setup.Token, newUser("x@example.com")); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("invite reused: %v", err)
	}

	inv, _ := s.CreateInvite(ctx, "bob@example.com", false, admin.ID, InviteLength)
	if err := s.CreateUser(ctx, inv.Token, newUser("eve@example.com")); !errors.Is(err, ErrInviteEmail) {
		t.Fatalf("wrong email accepted: %v", err)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if err := s.CreateUser(ctx, inv.Token, newUser("bob@example.com")); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("expired invite accepted: %v", err)
	}
}

func TestSessions(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	inv, _ := s.SetupInvite(ctx)
	u := newUser("a@example.com")
	s.CreateUser(ctx, inv.Token, u)
	tok, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionUser(ctx, tok)
	if err != nil || got.ID != u.ID {
		t.Fatalf("%v %v", got, err)
	}
	if err := s.ResetCredentials(ctx, u.ID, Credentials{KDFSalt: "s2", KDFIterations: 2, AuthHash: "h2", AccountKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("session survived a password reset")
	}
	tok, _ = s.CreateSession(ctx, u.ID)
	*now = now.Add(SessionLength + time.Second)
	if _, err := s.SessionUser(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session works")
	}
}

func TestJoinAndPCs(t *testing.T) {
	s, now := open(t)
	ctx := context.Background()
	inv, _ := s.SetupInvite(ctx)
	u := newUser("a@example.com")
	s.CreateUser(ctx, inv.Token, u)

	code, _, err := s.CreateJoinCode(ctx, u.ID)
	if err != nil || len(code) != 6 {
		t.Fatalf("%q %v", code, err)
	}
	if pc, err := s.JoinStatus(ctx, u.ID, code); pc != nil || err != nil {
		t.Fatalf("%v %v", pc, err)
	}
	// Loose parsing: lowercase and a browser's " (1)" suffix still work.
	pc, token, err := s.Join(ctx, " "+code[:3]+"-"+code[3:]+" ", PC{Name: "laptop", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Join(ctx, code, PC{Name: "again"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("join code used twice")
	}
	if got, _ := s.JoinStatus(ctx, u.ID, code); got == nil || got.ID != pc.ID {
		t.Fatalf("status %v", got)
	}
	if _, err := s.AuthPC(ctx, pc.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthPC(ctx, pc.ID, "wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong token accepted")
	}
	if err := s.SetPCKey(ctx, pc.ID, "sealed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPCKey(ctx, pc.ID, "other"); !errors.Is(err, ErrKeySet) {
		t.Fatal("key replaced")
	}

	code2, _, _ := s.CreateJoinCode(ctx, u.ID)
	*now = now.Add(JoinCodeLength + time.Second)
	if _, _, err := s.Join(ctx, code2, PC{Name: "late"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired join code works")
	}

	b := Backup{PCID: pc.ID, Entry: "manual", StartedAt: *now, FinishedAt: now.Add(time.Minute), OK: true, Bytes: 10}
	if err := s.AddBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.OK = false
	b.FinishedAt = now.Add(2 * time.Minute)
	s.AddBackup(ctx, b)
	last, lastOK, err := s.LastBackups(ctx)
	if err != nil || !last[pc.ID].Equal(now.Add(2*time.Minute)) || !lastOK[pc.ID].Equal(now.Add(time.Minute)) {
		t.Fatalf("%v %v %v", last, lastOK, err)
	}
	recent, _ := s.BackupsSince(ctx, now.Add(-time.Hour))
	if len(recent[pc.ID]) != 2 {
		t.Fatalf("%v", recent)
	}
}

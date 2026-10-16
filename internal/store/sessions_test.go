package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestSessionStore exercises the SQLite side of browser sign-in through the
// auth.Sessions logic, plus the store rules the logic relies on.
func TestSessionStore(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	s := auth.NewSessions(db)

	link, _, err := s.NewLoginLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Exchange(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Valid(ctx, token); !ok || err != nil {
		t.Fatalf("valid = %v, %v", ok, err)
	}
	if _, _, err := s.Exchange(ctx, link); !errors.Is(err, auth.ErrLinkUsed) {
		t.Fatalf("second exchange: %v", err)
	}

	// UseLoginLink is atomic: the second caller loses.
	at := time.Now().UTC()
	if err := db.CreateLoginLink(ctx, auth.LoginLink{Hash: "h1", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLoginLink(ctx, auth.LoginLink{Hash: "h1", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}); !errors.Is(err, change.ErrConflict) {
		t.Fatalf("duplicate hash: %v", err)
	}
	first, _ := db.UseLoginLink(ctx, "h1", at)
	second, _ := db.UseLoginLink(ctx, "h1", at)
	if !first || second {
		t.Fatalf("use twice = %v, %v; want true then false", first, second)
	}
	if used, _ := db.UseLoginLink(ctx, "missing", at); used {
		t.Fatal("using an unknown link reported success")
	}
	l, ok, err := db.GetLoginLink(ctx, "h1")
	if err != nil || !ok || l.UsedAt == nil || !l.UsedAt.Equal(at) || !l.ExpiresAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("link = %+v, %v, %v", l, ok, err)
	}
	if _, ok, _ := db.GetLoginLink(ctx, "missing"); ok {
		t.Fatal("unknown link found")
	}

	// Sweep removes only what expired.
	if err := db.CreateSession(ctx, auth.Session{Hash: "old", CreatedAt: at, ExpiresAt: at.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, auth.Session{Hash: "new", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteExpired(ctx, at); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.GetSession(ctx, "old"); ok {
		t.Error("expired session survived the sweep")
	}
	if _, ok, _ := db.GetSession(ctx, "new"); !ok {
		t.Error("live session was swept")
	}
	if _, ok, _ := db.GetLoginLink(ctx, "h1"); !ok {
		t.Error("unexpired link was swept")
	}

	if err := db.DeleteSession(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.DeleteAllSessions(ctx); err != nil || n != 1 {
		t.Fatalf("DeleteAllSessions = %d, %v; want the one real session left", n, err)
	}
	if ok, _ := s.Valid(ctx, token); ok {
		t.Fatal("session valid after revoking all")
	}
}

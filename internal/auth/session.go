package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Lifetimes of browser sign-in material.
const (
	LinkTTL    = 10 * time.Minute    // a one-time login link
	SessionTTL = 30 * 24 * time.Hour // a browser session
)

// Exchange failures. Each is reported to the browser by name.
var (
	ErrLinkInvalid = errors.New("login link is not known")
	ErrLinkUsed    = errors.New("login link was already used")
	ErrLinkExpired = errors.New("login link expired")
)

// LoginLink is a one-time token the CLI printed. Only its hash is stored.
type LoginLink struct {
	Hash      string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// Session is a signed-in browser. Only the hash of its cookie is stored.
type Session struct {
	Hash      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionStore persists login links and sessions by token hash.
type SessionStore interface {
	CreateLoginLink(ctx context.Context, l LoginLink) error
	GetLoginLink(ctx context.Context, hash string) (LoginLink, bool, error)
	// UseLoginLink marks the link used and reports whether this call did
	// it; false when it was used before or does not exist.
	UseLoginLink(ctx context.Context, hash string, at time.Time) (bool, error)
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, hash string) (Session, bool, error)
	DeleteSession(ctx context.Context, hash string) error
	DeleteAllSessions(ctx context.Context) (int, error)
	// DeleteExpired removes links and sessions whose expiry is not after now.
	DeleteExpired(ctx context.Context, now time.Time) error
}

// Sessions signs browsers in with one-time links and keeps their sessions.
type Sessions struct {
	store SessionStore
	now   func() time.Time
}

// NewSessions returns a Sessions backed by store.
func NewSessions(store SessionStore) *Sessions {
	return &Sessions{store: store, now: time.Now}
}

// WithClock makes s read the time from now. Tests use it to expire things.
func (s *Sessions) WithClock(now func() time.Time) *Sessions {
	s.now = now
	return s
}

// NewLoginLink creates a one-time link token and returns it with its expiry.
// Expired links and sessions are swept at the same time.
func (s *Sessions) NewLoginLink(ctx context.Context) (string, time.Time, error) {
	now := s.now().UTC()
	if err := s.store.DeleteExpired(ctx, now); err != nil {
		return "", time.Time{}, err
	}
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(LinkTTL)
	if err := s.store.CreateLoginLink(ctx, LoginLink{Hash: hashToken(token), CreatedAt: now, ExpiresAt: expires}); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Exchange turns an unused, unexpired login link into a session token.
func (s *Sessions) Exchange(ctx context.Context, link string) (string, time.Time, error) {
	now := s.now().UTC()
	l, ok, err := s.store.GetLoginLink(ctx, hashToken(link))
	if err != nil {
		return "", time.Time{}, err
	}
	switch {
	case !ok:
		return "", time.Time{}, ErrLinkInvalid
	case l.UsedAt != nil:
		return "", time.Time{}, ErrLinkUsed
	case !l.ExpiresAt.After(now):
		return "", time.Time{}, ErrLinkExpired
	}
	used, err := s.store.UseLoginLink(ctx, l.Hash, now)
	if err != nil {
		return "", time.Time{}, err
	}
	if !used {
		return "", time.Time{}, ErrLinkUsed
	}
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(SessionTTL)
	if err := s.store.CreateSession(ctx, Session{Hash: hashToken(token), CreatedAt: now, ExpiresAt: expires}); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Valid reports whether token names a session that has not expired.
func (s *Sessions) Valid(ctx context.Context, token string) (bool, error) {
	sess, ok, err := s.store.GetSession(ctx, hashToken(token))
	if err != nil || !ok {
		return false, err
	}
	return sess.ExpiresAt.After(s.now()), nil
}

// Expiry returns when the session named by token ends; ok is false for an
// unknown or expired session.
func (s *Sessions) Expiry(ctx context.Context, token string) (time.Time, bool, error) {
	sess, ok, err := s.store.GetSession(ctx, hashToken(token))
	if err != nil || !ok || !sess.ExpiresAt.After(s.now()) {
		return time.Time{}, false, err
	}
	return sess.ExpiresAt, true, nil
}

// Revoke ends one session. Unknown tokens are not an error.
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashToken(token))
}

// RevokeAll ends every session and returns how many there were.
func (s *Sessions) RevokeAll(ctx context.Context) (int, error) {
	return s.store.DeleteAllSessions(ctx)
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken is what the store sees instead of a token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

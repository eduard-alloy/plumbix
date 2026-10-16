package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// memSessions is an in-memory SessionStore for these tests. The SQLite
// implementation has its own tests in the store package.
type memSessions struct {
	links    map[string]LoginLink
	sessions map[string]Session
}

func newMem() *memSessions {
	return &memSessions{links: map[string]LoginLink{}, sessions: map[string]Session{}}
}

func (m *memSessions) CreateLoginLink(_ context.Context, l LoginLink) error {
	m.links[l.Hash] = l
	return nil
}

func (m *memSessions) GetLoginLink(_ context.Context, hash string) (LoginLink, bool, error) {
	l, ok := m.links[hash]
	return l, ok, nil
}

func (m *memSessions) UseLoginLink(_ context.Context, hash string, at time.Time) (bool, error) {
	l, ok := m.links[hash]
	if !ok || l.UsedAt != nil {
		return false, nil
	}
	l.UsedAt = &at
	m.links[hash] = l
	return true, nil
}

func (m *memSessions) CreateSession(_ context.Context, s Session) error {
	m.sessions[s.Hash] = s
	return nil
}

func (m *memSessions) GetSession(_ context.Context, hash string) (Session, bool, error) {
	s, ok := m.sessions[hash]
	return s, ok, nil
}

func (m *memSessions) DeleteSession(_ context.Context, hash string) error {
	delete(m.sessions, hash)
	return nil
}

func (m *memSessions) DeleteAllSessions(context.Context) (int, error) {
	n := len(m.sessions)
	m.sessions = map[string]Session{}
	return n, nil
}

func (m *memSessions) DeleteExpired(_ context.Context, now time.Time) error {
	for h, l := range m.links {
		if !l.ExpiresAt.After(now) {
			delete(m.links, h)
		}
	}
	for h, s := range m.sessions {
		if !s.ExpiresAt.After(now) {
			delete(m.sessions, h)
		}
	}
	return nil
}

var ctx = context.Background()

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func newSessions(t *testing.T) (*Sessions, *memSessions, *time.Time) {
	t.Helper()
	mem := newMem()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s := NewSessions(mem)
	s.now = func() time.Time { return now }
	return s, mem, &now
}

func TestLoginLinkSignsInOnce(t *testing.T) {
	s, mem, now := newSessions(t)
	link, expires, err := s.NewLoginLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !tokenShape.MatchString(link) || !expires.Equal(now.Add(LinkTTL)) {
		t.Fatalf("link %q expires %v; want a 43-char token expiring in %v", link, expires, LinkTTL)
	}
	for h := range mem.links {
		if strings.Contains(h, link) || h == link {
			t.Fatal("the store holds the raw link token")
		}
	}
	session, sessExpires, err := s.Exchange(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if !tokenShape.MatchString(session) || session == link || !sessExpires.Equal(now.Add(SessionTTL)) {
		t.Fatalf("session %q expires %v", session, sessExpires)
	}
	for h := range mem.sessions {
		if h == session {
			t.Fatal("the store holds the raw session token")
		}
	}
	if ok, err := s.Valid(ctx, session); !ok || err != nil {
		t.Fatalf("fresh session valid = %v, %v", ok, err)
	}
	if _, _, err := s.Exchange(ctx, link); !errors.Is(err, ErrLinkUsed) {
		t.Fatalf("second exchange: %v, want ErrLinkUsed", err)
	}
	if _, _, err := s.Exchange(ctx, "nope"); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("unknown link: %v, want ErrLinkInvalid", err)
	}
}

func TestLoginLinkExpires(t *testing.T) {
	s, _, now := newSessions(t)
	link, _, err := s.NewLoginLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(LinkTTL + time.Second)
	if _, _, err := s.Exchange(ctx, link); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("expired link: %v, want ErrLinkExpired", err)
	}
	// Used wins over expired: the link was consumed, say so.
	fresh, _, _ := s.NewLoginLink(ctx)
	if _, _, err := s.Exchange(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(LinkTTL + time.Second)
	if _, _, err := s.Exchange(ctx, fresh); !errors.Is(err, ErrLinkUsed) {
		t.Fatalf("used then expired: %v, want ErrLinkUsed", err)
	}
}

func TestSessionsExpireAndRevoke(t *testing.T) {
	s, mem, now := newSessions(t)
	link, _, _ := s.NewLoginLink(ctx)
	a, _, _ := s.Exchange(ctx, link)
	link2, _, _ := s.NewLoginLink(ctx)
	b, _, _ := s.Exchange(ctx, link2)

	if ok, _ := s.Valid(ctx, "not-a-session"); ok {
		t.Fatal("unknown session accepted")
	}
	if err := s.Revoke(ctx, a); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Valid(ctx, a); ok {
		t.Fatal("revoked session still valid")
	}
	if ok, _ := s.Valid(ctx, b); !ok {
		t.Fatal("revoking one session ended another")
	}
	*now = now.Add(SessionTTL + time.Second)
	if ok, _ := s.Valid(ctx, b); ok {
		t.Fatal("session valid after its expiry")
	}
	link3, _, _ := s.NewLoginLink(ctx) // creating a link sweeps expired rows
	if _, ok := mem.sessions[hashToken(b)]; ok {
		t.Error("expired session not swept")
	}
	c, _, _ := s.Exchange(ctx, link3)
	if n, err := s.RevokeAll(ctx); err != nil || n != 1 {
		t.Fatalf("RevokeAll = %d, %v; want 1", n, err)
	}
	if ok, _ := s.Valid(ctx, c); ok {
		t.Fatal("session valid after RevokeAll")
	}
}

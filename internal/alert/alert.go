// Package alert holds the things a human should look at: commits that
// reached a protected branch without a verdict, and tokens about to expire.
package alert

import (
	"context"
	"time"
)

// Kind is an alert's type.
type Kind string

// Alert kinds.
const (
	BypassCommit Kind = "bypass_commit"
	TokenExpiry  Kind = "token_expiry"
)

// Alert is one open or resolved alert. Kind and Key identify its subject:
// only one alert per subject is open at a time.
type Alert struct {
	ID         string         `json:"id"`
	Kind       Kind           `json:"kind"`
	Key        string         `json:"key"`
	RepoID     string         `json:"repo_id,omitempty"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	ResolvedAt *time.Time     `json:"resolved_at,omitempty"`
}

// Store persists alerts.
type Store interface {
	// Raise opens an alert unless one with the same kind and key is open.
	// It returns the open alert and whether this call created it.
	Raise(ctx context.Context, a Alert) (Alert, bool, error)
	// Resolve closes an alert by ID; change.ErrNotFound if there is none.
	// Resolving a resolved alert changes nothing.
	Resolve(ctx context.Context, id string, at time.Time) error
	// ResolveKey closes the open alert for a kind and key, if any.
	ResolveKey(ctx context.Context, kind Kind, key string, at time.Time) error
	// Get returns one alert; change.ErrNotFound if there is none.
	Get(ctx context.Context, id string) (Alert, error)
	// List returns alerts newest first, open ones only unless all is set.
	List(ctx context.Context, all bool) ([]Alert, error)
}

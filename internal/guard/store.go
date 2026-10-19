package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Approval is a pipeline file's content a person approved (WU-051, A38).
type Approval struct {
	Path   string
	Digest string
}

// Store keeps the approved pipeline files of each repository.
type Store interface {
	// ApprovePipelines records files as approved by a change; changeID is
	// empty for trust on first use.
	ApprovePipelines(ctx context.Context, repoID string, files []Approval, changeID string, at time.Time) error
	// PipelineApproved reports whether path's content with this digest was approved.
	PipelineApproved(ctx context.Context, repoID, path, digest string) (bool, error)
	// HasPipelineApprovals reports whether the repository has any approval.
	HasPipelineApprovals(ctx context.Context, repoID string) (bool, error)
}

// Digest is the SHA-256 of a pipeline file's content, hex encoded.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

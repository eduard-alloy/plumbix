package runner

import (
	"testing"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

// TestWorkspacePathMatchesValidatorContract keeps the runner's mount point
// equal to the path the validator contract documents.
func TestWorkspacePathMatchesValidatorContract(t *testing.T) {
	if WorkspacePath != validation.WorkspacePath {
		t.Fatalf("runner mounts the workspace at %q but the validator contract says %q", WorkspacePath, validation.WorkspacePath)
	}
}

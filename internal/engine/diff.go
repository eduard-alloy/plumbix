package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/diff"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// maxDiffBytes bounds the unified diff that is parsed into hunks. Larger
// diffs are returned as stats only.
const maxDiffBytes = 4 << 20

// Diff returns base..head of the change's latest run that has a head: the
// diff the validators judged. withHunks adds parsed hunks unless the diff is
// larger than maxDiffBytes, in which case the result is marked truncated.
func (e *Engine) Diff(ctx context.Context, changeID string, withHunks bool) (diff.Diff, error) {
	c, err := e.store.GetChange(ctx, changeID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return diff.Diff{}, fmt.Errorf("%w: change %s", change.ErrNotFound, changeID)
		}
		return diff.Diff{}, err
	}
	runs, err := e.store.ListRuns(ctx, c.ID)
	if err != nil {
		return diff.Diff{}, err
	}
	var latest *run.Run
	for i := len(runs) - 1; i >= 0 && latest == nil; i-- {
		if runs[i].Head != "" && runs[i].Base != "" {
			latest = &runs[i]
		}
	}
	if latest == nil {
		return diff.Diff{}, fmt.Errorf("%w: change %s has no validated commit yet", change.ErrInvalidState, c.ID)
	}
	repo, err := e.store.GetRepo(ctx, c.RepoID)
	if err != nil {
		return diff.Diff{}, err
	}
	m, err := e.mirror(repo)
	if err != nil {
		return diff.Diff{}, err
	}
	files, err := m.DiffStat(ctx, latest.Base, latest.Head)
	if err != nil {
		return diff.Diff{}, err
	}
	d := diff.Diff{Base: latest.Base, Head: latest.Head, Files: files}
	d.Total()
	if !withHunks {
		return d, nil
	}
	unified, err := m.Diff(ctx, latest.Base, latest.Head)
	if err != nil {
		return diff.Diff{}, err
	}
	if len(unified) > maxDiffBytes {
		d.Truncated = true
		return d, nil
	}
	parsed, err := diff.Parse(unified)
	if err != nil {
		return diff.Diff{}, fmt.Errorf("parse diff %s..%s: %w", short(latest.Base), short(latest.Head), err)
	}
	hunks := map[string][]diff.Hunk{}
	for _, p := range parsed {
		hunks[p.Path] = p.Hunks
	}
	for i := range d.Files {
		d.Files[i].Hunks = hunks[d.Files[i].Path]
	}
	return d, nil
}

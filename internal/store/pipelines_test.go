package store

import (
	"testing"

	"github.com/eduard-alloy/plumbix/internal/guard"
)

// TestApprovedPipelines (WU-051): an approval is per repository, path and
// content; approving the same content twice keeps the first record.
func TestApprovedPipelines(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if has, err := db.HasPipelineApprovals(ctx, "r_1"); err != nil || has {
		t.Fatalf("has = %v, %v", has, err)
	}
	files := []guard.Approval{{Path: ".plumbix/pipelines/deploy.yaml", Digest: "aaa"}}
	if err := db.ApprovePipelines(ctx, "r_1", files, "c_1", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.ApprovePipelines(ctx, "r_1", files, "c_2", t0); err != nil {
		t.Fatalf("approving the same content again: %v", err)
	}
	for _, c := range []struct {
		path, digest string
		want         bool
	}{
		{".plumbix/pipelines/deploy.yaml", "aaa", true},
		{".plumbix/pipelines/deploy.yaml", "bbb", false},
		{".plumbix/pipeline.yaml", "aaa", false},
	} {
		if got, err := db.PipelineApproved(ctx, "r_1", c.path, c.digest); err != nil || got != c.want {
			t.Errorf("approved(%s, %s) = %v, %v", c.path, c.digest, got, err)
		}
	}
	if has, _ := db.HasPipelineApprovals(ctx, "r_1"); !has {
		t.Error("no approvals after approving")
	}
}

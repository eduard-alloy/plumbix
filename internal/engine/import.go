package engine

import (
	"context"
	"path"

	"github.com/eduard-alloy/plumbix/internal/importer"
)

// MaxImportFile is the largest file the importer reads.
const MaxImportFile = 1 << 20

// ImportView is a preview of importing a branch head's pipelines (WU-046).
type ImportView struct {
	Branch string
	SHA    string
	Result importer.Result
}

// ImportPreview converts the source system's pipelines at a branch head.
// It reads YAML files only and writes nothing.
func (e *Engine) ImportPreview(ctx context.Context, repoID, branch string, src importer.Source) (ImportView, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return ImportView{}, err
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return ImportView{}, err
	}
	head, err := m.Head(ctx, branch)
	if err != nil {
		return ImportView{}, err
	}
	tree, err := m.ListTree(ctx, head)
	if err != nil {
		return ImportView{}, err
	}
	files := map[string][]byte{}
	var big []importer.Problem
	for _, f := range tree {
		if ext := path.Ext(f.Path); ext != ".yml" && ext != ".yaml" {
			continue
		}
		if f.Size > MaxImportFile {
			big = append(big, importer.Problem{SourceFile: f.Path, Message: "larger than 1 MiB; not read"})
			continue
		}
		content, _, err := m.Show(ctx, head, f.Path)
		if err != nil {
			return ImportView{}, err
		}
		files[f.Path] = content
	}
	r := importer.Preview(src, files)
	r.Problems = append(big, r.Problems...)
	return ImportView{Branch: branch, SHA: head, Result: r}, nil
}

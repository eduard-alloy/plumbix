package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Folder is what Plumbix reads from a local working tree before importing
// it (WU-041): its remote, its default branch and whether it has a pipeline.
type Folder struct {
	Path          string `json:"path"`
	Remote        string `json:"remote"` // origin's URL, else the first remote's; "" without one
	DefaultBranch string `json:"default_branch"`
	HasPipeline   bool   `json:"has_pipeline"`
}

// PipelinePath is where a repository keeps its pipeline file.
const PipelinePath = ".plumbix/pipeline.yaml"

// InspectFolder reads a working tree through git only. It changes nothing.
func InspectFolder(ctx context.Context, path string) (Folder, error) {
	m := &Mirror{dir: path}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return Folder{}, fmt.Errorf("%s is not a folder", path)
	}
	if out, err := m.git(ctx, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(string(out)) != "true" {
		return Folder{}, fmt.Errorf("%s is not a git working tree", path)
	}
	f := Folder{Path: path}
	if out, err := m.git(ctx, "remote"); err == nil {
		remotes := strings.Fields(string(out))
		name := ""
		for _, r := range remotes {
			if r == "origin" {
				name = r
			}
		}
		if name == "" && len(remotes) > 0 {
			name = remotes[0]
		}
		if name != "" {
			if url, err := m.git(ctx, "remote", "get-url", name); err == nil {
				f.Remote = strings.TrimSpace(string(url))
			}
			if head, err := m.git(ctx, "symbolic-ref", "--short", "refs/remotes/"+name+"/HEAD"); err == nil {
				f.DefaultBranch = strings.TrimPrefix(strings.TrimSpace(string(head)), name+"/")
			}
		}
	}
	if f.DefaultBranch == "" {
		if head, err := m.git(ctx, "symbolic-ref", "--short", "HEAD"); err == nil {
			f.DefaultBranch = strings.TrimSpace(string(head))
		}
	}
	if _, err := m.git(ctx, "cat-file", "-e", "HEAD:"+PipelinePath); err == nil {
		f.HasPipeline = true
	}
	return f, nil
}

// ErrNotEmpty means a clone's destination already holds files.
var ErrNotEmpty = errors.New("the destination folder is not empty")

// Clone clones url into dest, which must not exist or be an empty folder;
// its parent must exist. Credentials, when given, reach git through the
// askpass script and the process environment only, never the arguments or
// the clone's configuration.
func Clone(ctx context.Context, url, dest string, auth *Auth, askpass string) error {
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrNotEmpty, dest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m := &Mirror{}
	if auth != nil {
		m.WithAuth(*auth, askpass)
	}
	if _, err := m.git(ctx, "clone", "-q", "--", url, dest); err != nil {
		return err
	}
	return nil
}

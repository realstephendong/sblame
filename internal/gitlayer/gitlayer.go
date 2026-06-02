// Package gitlayer provides wrappers around go-git for repository access.
package gitlayer

import (
	"errors"

	"github.com/go-git/go-git/v5"
	"github.com/realstephendong/sblame/internal/types"
)

// ErrNotImplemented is returned by stub functions that have no implementation yet.
var ErrNotImplemented = errors.New("gitlayer: not implemented")

// Repo wraps a go-git repository with sblame-specific accessors.
type Repo struct {
	repo *git.Repository
}

// Open opens a git repository at the given filesystem path.
func Open(path string) (*Repo, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return nil, err
	}
	return &Repo{repo: r}, nil
}

// WalkParents yields parent commits of the given commit hash in topological
// order. The caller receives commits one at a time via the callback.
//
// TODO: handle multi-parent (merge) commits. Currently this is a stub that
// returns ErrNotImplemented.
func (r *Repo) WalkParents(commitHash string, fn func(types.Commit) error) error {
	return ErrNotImplemented
}

// DiffCommits returns the unified diff between two commits as raw bytes.
//
// This is a stub.
func (r *Repo) DiffCommits(parentHash, childHash string) ([]byte, error) {
	return nil, ErrNotImplemented
}

// ReadBlob reads the contents of a file at a specific commit.
//
// This is a stub.
func (r *Repo) ReadBlob(commitHash, filePath string) ([]byte, error) {
	return nil, ErrNotImplemented
}

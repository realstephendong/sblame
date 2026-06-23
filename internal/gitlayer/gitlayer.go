// Package gitlayer provides wrappers around go-git for repository access.
//
// It exposes the low-level primitives the blame engine walks on: resolving a
// commit by hash, stepping to a commit's parent, reading a file's contents at a
// given commit, and producing a structured line-level diff of a file between a
// commit and its parent. The recursive blame walk and range tracking live in
// the blame package; this layer stays at the "navigate history, read blobs, and
// describe a single-file change" level.
//
// Hashes cross this package's API boundary as plain strings. plumbing.Hash is
// an internal detail and is never part of an exported signature.
package gitlayer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Sentinel errors. Callers branch on these with errors.Is, so they are defined
// here rather than as inline fmt.Errorf strings.
var (
	// ErrCommitNotFound is returned by CommitByHash when no commit with the
	// given hash exists in the repository (this also covers malformed hash
	// strings, which resolve to a hash that is not present).
	ErrCommitNotFound = errors.New("gitlayer: commit not found")

	// ErrRootCommit is returned by Parent when the commit has no parents. It is
	// the base case of the blame walk: the caller stops and attributes the
	// tracked range to this commit.
	ErrRootCommit = errors.New("gitlayer: commit has no parents (root)")

	// ErrFileNotFound is returned by FileAt when the path does not exist in the
	// commit's tree. The blame engine treats this as a distinct signal — a file
	// being absent at a commit is meaningful: it triggers rename detection (see
	// RenameSource) rather than being a generic failure.
	ErrFileNotFound = errors.New("gitlayer: file not found at commit")
)

// Repo wraps a go-git repository with sblame-specific accessors.
type Repo struct {
	repo *git.Repository
}

// Open opens the git repository containing the given filesystem path. Parent
// directories are searched (DetectDotGit), so the path may be anywhere inside a
// working tree, not just its root.
func Open(path string) (*Repo, error) {
	r, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, err
	}
	return &Repo{repo: r}, nil
}

// ResolveCommit resolves a revision string — "HEAD", a branch or tag name, or a
// full/abbreviated hash — to its commit object.
func (r *Repo) ResolveCommit(rev string) (*object.Commit, error) {
	h, err := r.repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return nil, fmt.Errorf("gitlayer: resolving revision %q: %w", rev, err)
	}
	commit, err := r.repo.CommitObject(*h)
	if err != nil {
		return nil, fmt.Errorf("gitlayer: loading commit %s: %w", h.String(), err)
	}
	return commit, nil
}

// Root returns the absolute path to the repository's working-tree root, used to
// translate filesystem paths into repository-relative paths.
func (r *Repo) Root() (string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("gitlayer: obtaining worktree: %w", err)
	}
	return wt.Filesystem.Root(), nil
}

// CommitByHash resolves a hex commit hash to its commit object.
//
// The hash crosses the API as a string and is converted to a plumbing.Hash
// internally. If the commit does not exist — including when hash is malformed
// and resolves to a hash that is absent from the object store — it returns an
// error wrapping ErrCommitNotFound.
func (r *Repo) CommitByHash(hash string) (*object.Commit, error) {
	commit, err := r.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrCommitNotFound, hash)
		}
		return nil, fmt.Errorf("gitlayer: resolving commit %s: %w", hash, err)
	}
	return commit, nil
}

// Parent returns the commit to continue the blame walk into.
//
// Merge handling is explicit:
//
//   - 0 parents: returns ErrRootCommit. This is the recursion base case; the
//     caller stops walking and attributes the tracked range here.
//   - 1 parent: returns that parent.
//   - >1 parents (a merge commit): returns the FIRST parent only, matching the
//     default behavior of `git blame`.
//
// TODO(Month 3): the first-parent-only rule for merges is a Month 1
// simplification and is deliberately visible here rather than silent. Any logic
// that was actually introduced on a merged-in (non-first-parent) branch will be
// misattributed to the merge commit's author, because we never look down the
// other parents. Correct attribution requires diffing the tracked line range
// against EACH parent to determine which branch genuinely introduced the
// change, then descending into that branch. That depends on the diff/line-range
// layer, which is out of scope for this primitive.
func (r *Repo) Parent(commit *object.Commit) (*object.Commit, error) {
	switch commit.NumParents() {
	case 0:
		return nil, ErrRootCommit
	default:
		// Covers both the single-parent case and the merge case: in both we
		// follow the first parent. See the merge TODO above.
		parent, err := commit.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("gitlayer: loading first parent of %s: %w", commit.Hash.String(), err)
		}
		return parent, nil
	}
}

// FileAt returns the contents of path at the given commit, split into lines.
//
// If path does not exist in the commit's tree it returns ErrFileNotFound, which
// the engine distinguishes from other failures.
//
// Trailing-newline handling (kept consistent so downstream 1-based line-range
// math is unambiguous):
//
//   - A truly empty file (zero bytes) yields an empty slice — zero lines.
//   - Otherwise the content is split on "\n", and a single trailing "\n" (the
//     normal POSIX line terminator) is NOT counted as an extra empty final
//     line. So "a\nb\n" and "a\nb" both yield []string{"a", "b"} (2 lines),
//     and "\n" yields []string{""} (1 empty line).
//
// The returned slice index i corresponds to 1-based line number i+1.
func (r *Repo) FileAt(commit *object.Commit, path string) ([]string, error) {
	f, err := commit.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s @ %s", ErrFileNotFound, path, commit.Hash.String())
		}
		return nil, fmt.Errorf("gitlayer: locating %s @ %s: %w", path, commit.Hash.String(), err)
	}

	content, err := f.Contents()
	if err != nil {
		return nil, fmt.Errorf("gitlayer: reading %s @ %s: %w", path, commit.Hash.String(), err)
	}

	return splitLines(content), nil
}

// splitLines turns file content into lines, treating a single trailing newline
// as a terminator rather than the start of an empty final line. See FileAt's
// doc comment for the exact contract.
func splitLines(content string) []string {
	if content == "" {
		return []string{}
	}
	content = strings.TrimSuffix(content, "\n")
	return strings.Split(content, "\n")
}

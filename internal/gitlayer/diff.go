package gitlayer

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// Line-numbering convention for everything in this file:
//
//   - Line numbers are 1-based, matching FileAt's line model (see FileAt for the
//     trailing-newline contract). The diff is computed over the exact []string
//     slices FileAt returns; content is never re-split with different rules here.
//   - A Hunk's ParentStart/ChildStart are 1-based line numbers of the first line
//     the hunk covers on that side. ParentLen/ChildLen are line COUNTS, so the
//     covered lines are [Start, Start+Len-1] inclusive.
//   - A zero-length side (Len == 0) carries the position at which the change
//     occurs: Start is the line number of the line that follows the change on
//     that side (== len(side)+1 when the change is at the very end). For an
//     Added hunk this is the parent insertion point; for a Deleted hunk it is
//     the child position where the removed lines used to be.
//
// The diff always describes the transformation parent -> child.

// ChangeKind classifies how a single hunk's lines changed from parent to child.
type ChangeKind int

const (
	// Equal: the lines are identical in parent and child and correspond 1:1.
	// ParentLen == ChildLen. Child line ChildStart+k maps to parent line
	// ParentStart+k.
	Equal ChangeKind = iota

	// Added: lines present in the child with no parent counterpart
	// (ParentLen == 0). These child lines were newly introduced at the child.
	Added

	// Deleted: lines present in the parent that are gone in the child
	// (ChildLen == 0). These occupy no child lines.
	Deleted

	// Modified: a block of parent lines was replaced by a block of child lines
	// (ParentLen > 0 and ChildLen > 0, no 1:1 correspondence). The child lines
	// are considered introduced at the child; the parent lines were removed.
	Modified
)

// String returns the human-readable name of a ChangeKind value.
func (k ChangeKind) String() string {
	switch k {
	case Equal:
		return "EQUAL"
	case Added:
		return "ADDED"
	case Deleted:
		return "DELETED"
	case Modified:
		return "MODIFIED"
	default:
		return "UNKNOWN"
	}
}

// FileStatus describes the file-level change between parent and child, so a
// caller can distinguish the whole-file cases without inspecting the hunks.
type FileStatus int

const (
	// FileUnchanged: the file's contents are identical in parent and child.
	// The hunk list is a single identity Equal hunk (or empty for an empty file).
	FileUnchanged FileStatus = iota

	// FileAdded: the file exists in the child but not in the parent. Hunks
	// describe every child line as Added.
	FileAdded

	// FileDeleted: the file exists in the parent but not in the child. Hunks
	// describe every parent line as Deleted.
	FileDeleted

	// FileModified: the file exists in both and its contents differ.
	FileModified
)

// String returns the human-readable name of a FileStatus value.
func (s FileStatus) String() string {
	switch s {
	case FileUnchanged:
		return "UNCHANGED"
	case FileAdded:
		return "ADDED"
	case FileDeleted:
		return "DELETED"
	case FileModified:
		return "MODIFIED"
	default:
		return "UNKNOWN"
	}
}

// Hunk maps a contiguous block of parent lines to a contiguous block of child
// lines. See the line-numbering convention at the top of this file.
type Hunk struct {
	// Kind is how this block changed from parent to child.
	Kind ChangeKind

	// ParentStart is the 1-based first parent line the hunk covers.
	ParentStart int
	// ParentLen is the number of parent lines covered (0 for Added).
	ParentLen int

	// ChildStart is the 1-based first child line the hunk covers.
	ChildStart int
	// ChildLen is the number of child lines covered (0 for Deleted).
	ChildLen int
}

// FileDiff is the structured, go-git-free description of how a single file
// changed from a parent commit to a child commit.
//
// The Hunks partition both the parent's and the child's line space in order and
// without gaps: reading them left to right, ParentStart/ChildStart advance
// monotonically. A caller can take a child line N, find the hunk whose child
// range contains it (Deleted hunks contain no child lines), and answer:
//
//   - Equal hunk: parent line = ParentStart + (N - ChildStart).
//   - Added or Modified hunk: N was introduced at the child (no parent line).
//
// Lines added or removed above a range are accounted for automatically, because
// each Equal hunk carries its own ParentStart reflecting the cumulative shift.
type FileDiff struct {
	// Path is the repository-relative file path that was diffed.
	Path string

	// Status is the file-level change (added/deleted/unchanged/modified).
	Status FileStatus

	// Hunks is the ordered line-level mapping from parent to child.
	Hunks []Hunk
}

// DiffFile returns a structured line-level diff of path between a parent commit
// and a child commit, describing the parent -> child transformation.
//
// It reuses FileAt for both versions so the newline/line model is identical to
// the rest of the package. The whole-file cases are reported via FileDiff.Status:
//
//   - file absent in the parent but present in the child  -> FileAdded
//   - file present in the parent but absent in the child  -> FileDeleted
//   - file present in both with identical contents        -> FileUnchanged
//   - file present in both with differing contents        -> FileModified
//
// If the path is absent in BOTH commits it returns an error wrapping
// ErrFileNotFound. Any non-"file not found" error from reading either version is
// returned as-is.
//
// Note: this is named DiffFile (not the old DiffCommits stub) because it
// describes a single file's change, consistent with FileAt operating on one path.
func (r *Repo) DiffFile(child, parent *object.Commit, path string) (*FileDiff, error) {
	parentLines, parentErr := r.FileAt(parent, path)
	if parentErr != nil && !errors.Is(parentErr, ErrFileNotFound) {
		return nil, parentErr
	}
	childLines, childErr := r.FileAt(child, path)
	if childErr != nil && !errors.Is(childErr, ErrFileNotFound) {
		return nil, childErr
	}

	parentMissing := errors.Is(parentErr, ErrFileNotFound)
	childMissing := errors.Is(childErr, ErrFileNotFound)

	if parentMissing && childMissing {
		return nil, fmt.Errorf("%w: %s in neither %s nor %s",
			ErrFileNotFound, path, child.Hash.String(), parent.Hash.String())
	}

	status := FileModified
	switch {
	case parentMissing:
		status = FileAdded
	case childMissing:
		status = FileDeleted
	case equalLines(parentLines, childLines):
		status = FileUnchanged
	}

	// When a side is missing its slice is nil; diffLines then yields a single
	// all-Added or all-Deleted hunk, consistent with the Status above.
	return &FileDiff{
		Path:   path,
		Status: status,
		Hunks:  diffLines(parentLines, childLines),
	}, nil
}

// opKind is a single-line edit step used while building the hunk list.
type opKind int

const (
	opEqual opKind = iota
	opDel          // a parent-only line (deletion)
	opIns          // a child-only line (insertion)
)

// diffLines computes a line-level diff from parent to child and returns the
// coalesced hunk list. Either slice may be empty (file added or deleted).
//
// It trims the common prefix and suffix first so the O(n*m) LCS table only
// covers the changed middle region, then backtracks the LCS into per-line edit
// steps, then coalesces those steps into hunks.
func diffLines(parent, child []string) []Hunk {
	// Common prefix.
	p0, c0 := 0, 0
	for p0 < len(parent) && c0 < len(child) && parent[p0] == child[c0] {
		p0++
		c0++
	}
	// Common suffix (not overlapping the prefix).
	pe, ce := len(parent), len(child)
	for pe > p0 && ce > c0 && parent[pe-1] == child[ce-1] {
		pe--
		ce--
	}

	ops := make([]opKind, 0, len(parent)+len(child))
	for k := 0; k < p0; k++ {
		ops = append(ops, opEqual)
	}
	ops = append(ops, lcsOps(parent[p0:pe], child[c0:ce])...)
	for k := pe; k < len(parent); k++ {
		ops = append(ops, opEqual)
	}

	return coalesce(ops)
}

// lcsOps returns the per-line edit steps transforming a (parent middle) into b
// (child middle), via a longest-common-subsequence backtrack. In a replaced
// region deletions are emitted before insertions, so coalesce can fold an
// adjacent deletion run + insertion run into a single Modified hunk.
func lcsOps(a, b []string) []opKind {
	n, m := len(a), len(b)
	if n == 0 {
		ops := make([]opKind, m)
		for i := range ops {
			ops[i] = opIns
		}
		return ops
	}
	if m == 0 {
		ops := make([]opKind, n)
		for i := range ops {
			ops[i] = opDel
		}
		return ops
	}

	// dp[i][j] = length of the LCS of a[i:] and b[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	ops := make([]opKind, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, opEqual)
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, opDel)
			i++
		default:
			ops = append(ops, opIns)
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, opDel)
	}
	for ; j < m; j++ {
		ops = append(ops, opIns)
	}
	return ops
}

// coalesce folds the per-line edit steps into hunks, tracking 1-based parent and
// child line cursors. A deletion run adjacent to an insertion run (in either
// order) becomes one Modified hunk; a lone deletion run is Deleted, a lone
// insertion run is Added, and an equal run is Equal.
func coalesce(ops []opKind) []Hunk {
	type run struct {
		kind opKind
		n    int
	}
	var runs []run
	for _, o := range ops {
		if len(runs) > 0 && runs[len(runs)-1].kind == o {
			runs[len(runs)-1].n++
		} else {
			runs = append(runs, run{kind: o, n: 1})
		}
	}

	var hunks []Hunk
	pp, cp := 1, 1 // next unconsumed parent / child line (1-based)
	for k := 0; k < len(runs); k++ {
		switch runs[k].kind {
		case opEqual:
			n := runs[k].n
			hunks = append(hunks, Hunk{
				Kind: Equal, ParentStart: pp, ParentLen: n, ChildStart: cp, ChildLen: n,
			})
			pp += n
			cp += n

		case opDel:
			del := runs[k].n
			if k+1 < len(runs) && runs[k+1].kind == opIns {
				ins := runs[k+1].n
				hunks = append(hunks, Hunk{
					Kind: Modified, ParentStart: pp, ParentLen: del, ChildStart: cp, ChildLen: ins,
				})
				pp += del
				cp += ins
				k++ // consumed the following insertion run
			} else {
				hunks = append(hunks, Hunk{
					Kind: Deleted, ParentStart: pp, ParentLen: del, ChildStart: cp, ChildLen: 0,
				})
				pp += del
			}

		case opIns:
			ins := runs[k].n
			if k+1 < len(runs) && runs[k+1].kind == opDel {
				del := runs[k+1].n
				hunks = append(hunks, Hunk{
					Kind: Modified, ParentStart: pp, ParentLen: del, ChildStart: cp, ChildLen: ins,
				})
				pp += del
				cp += ins
				k++ // consumed the following deletion run
			} else {
				hunks = append(hunks, Hunk{
					Kind: Added, ParentStart: pp, ParentLen: 0, ChildStart: cp, ChildLen: ins,
				})
				cp += ins
			}
		}
	}
	return hunks
}

// equalLines reports whether two line slices are identical.
func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

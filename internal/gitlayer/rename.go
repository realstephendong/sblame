package gitlayer

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// File-rename detection. When the blame walk finds the tracked file missing in a
// parent commit, the file may have been renamed; this code answers "what path
// did it have before?" so the walk can continue across the rename.
//
// The matching algorithm is ours (see Similarity / BestRenameMatch), in keeping
// with the project's from-scratch stance: go-git is used only to enumerate a
// commit's files and read their bytes — the raw object store — not to detect the
// rename itself (we deliberately do not call object.DetectRenames). This mirrors
// diff.go, which hand-rolls its own diff over contents go-git merely hands it.

// RenameThreshold is the minimum content similarity for two files to be
// considered a rename, matching git's default -M threshold of 50%.
const RenameThreshold = 0.5

// Similarity scores how much two files share, as a fraction in [0, 1]. It is the
// number of lines they have in common — counted as a multiset, so a line that
// appears k times on one side and j on the other contributes min(k, j) — divided
// by the larger file's line count. Identical content scores 1.0; wholly disjoint
// content scores 0.0.
//
// Two empty files score 1.0 (nothing distinguishes them); an empty file against
// a non-empty one scores 0.0.
//
// Using the larger line count as the denominator (rather than the union) means a
// small file fully contained in a much larger one does not score as a rename,
// which matches the intuition that the file grew substantially.
func Similarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}

	counts := make(map[string]int, len(a))
	for _, line := range a {
		counts[line]++
	}
	common := 0
	for _, line := range b {
		if counts[line] > 0 {
			counts[line]--
			common++
		}
	}

	denom := len(a)
	if len(b) > denom {
		denom = len(b)
	}
	return float64(common) / float64(denom)
}

// BestRenameMatch returns the candidate most similar to target whose similarity
// is at least threshold; ok is false when none clears it. candidates maps a path
// to that file's lines.
//
// Ties (equal scores) are broken by the lexicographically smaller path so the
// result is deterministic regardless of map iteration order — a tie means two
// candidates are equally good rename sources, so the choice between them is
// arbitrary anyway.
func BestRenameMatch(target []string, candidates map[string][]string, threshold float64) (path string, score float64, ok bool) {
	for cand, lines := range candidates {
		s := Similarity(target, lines)
		if s < threshold {
			continue
		}
		if !ok || s > score || (s == score && cand < path) {
			path, score, ok = cand, s, true
		}
	}
	return path, score, ok
}

// RenameSource reports whether newPath in child was renamed from a different path
// in parent. When it was, ok is true, oldPath is the pre-rename path, and score
// is the content similarity in (0, 1] (1.0 for an exact rename, where the blob is
// byte-identical). When no rename source clears RenameThreshold, ok is false and
// the caller treats newPath as newly introduced at child.
//
// Candidates are the files present in parent but absent in child (the deletions a
// rename would produce). An exact rename — a candidate whose blob hash equals
// newPath's — is matched first without reading any content.
func (r *Repo) RenameSource(child, parent *object.Commit, newPath string) (oldPath string, score float64, ok bool, err error) {
	childTree, err := child.Tree()
	if err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: tree of %s: %w", child.Hash.String(), err)
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: tree of %s: %w", parent.Hash.String(), err)
	}

	// The new file's blob hash, for the exact-rename fast path.
	newEntry, err := childTree.FindEntry(newPath)
	if err != nil {
		// newPath is absent in child: nothing to trace. (The blame engine only
		// calls this when newPath exists in child, so this is defensive.)
		return "", 0, false, nil
	}

	// Names present in the child tree, so the parent's deletions can be picked out.
	childNames := map[string]bool{}
	if err := childTree.Files().ForEach(func(f *object.File) error {
		childNames[f.Name] = true
		return nil
	}); err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: iterating child tree of %s: %w", child.Hash.String(), err)
	}

	// Deletion candidates: parent files with no counterpart in child. An exact
	// blob match short-circuits to a certain rename.
	var candidateNames []string
	if err := parentTree.Files().ForEach(func(f *object.File) error {
		if childNames[f.Name] {
			return nil
		}
		if f.Hash == newEntry.Hash {
			return errExactRename{path: f.Name}
		}
		candidateNames = append(candidateNames, f.Name)
		return nil
	}); err != nil {
		var exact errExactRename
		if errors.As(err, &exact) {
			return exact.path, 1.0, true, nil
		}
		return "", 0, false, fmt.Errorf("gitlayer: iterating parent tree of %s: %w", parent.Hash.String(), err)
	}

	if len(candidateNames) == 0 {
		return "", 0, false, nil
	}

	// No exact match; fall back to content similarity (our algorithm).
	newLines, err := r.FileAt(child, newPath)
	if err != nil {
		return "", 0, false, err
	}
	candidates := make(map[string][]string, len(candidateNames))
	for _, name := range candidateNames {
		lines, ferr := r.FileAt(parent, name)
		if ferr != nil {
			if errors.Is(ferr, ErrFileNotFound) {
				continue
			}
			return "", 0, false, ferr
		}
		candidates[name] = lines
	}

	oldPath, score, ok = BestRenameMatch(newLines, candidates, RenameThreshold)
	return oldPath, score, ok, nil
}

// errExactRename is a sentinel used to break out of a tree iteration early once
// an exact (identical-blob) rename source is found. plumbing.Hash comparison is
// a cheap array equality, so this lets the common pure-rename case avoid reading
// any file contents.
type errExactRename struct{ path string }

func (errExactRename) Error() string { return "exact rename found" }

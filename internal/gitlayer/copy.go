package gitlayer

import (
	"fmt"
	"sort"
	"unicode"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Copy/move detection. When the blame walk is about to attribute a block of
// lines that was ADDED to a file, those lines may have been moved or copied from
// another file the same commit changed (git's -C). This code finds that source
// file and where the block sits in it, so the walk can continue there instead of
// crediting the commit that merely relocated the code.
//
// As with rename detection, the matching is ours: go-git only enumerates a
// commit's files and reads their bytes. We deliberately match the spec's "moved
// from another file in the same commit" (git's first -C level) — the source must
// be a file this commit changed; copies from untouched files or other commits
// are out of scope here.

// MinCopyChars is the minimum non-whitespace content a block must have before it
// is treated as a possible move/copy source, matching git's default -C threshold
// of 40 characters. It keeps a trivial added line (a lone "}" or "return nil")
// from being mistaken for relocated code.
const MinCopyChars = 40

// BlockSubstance returns the number of non-whitespace runes in a block of lines.
func BlockSubstance(block []string) int {
	n := 0
	for _, line := range block {
		for _, r := range line {
			if !unicode.IsSpace(r) {
				n++
			}
		}
	}
	return n
}

// FindBlock returns the 1-based line at which needle first occurs as a
// contiguous run of lines within hay, comparing lines exactly. ok is false when
// needle is empty, longer than hay, or absent.
func FindBlock(hay, needle []string) (start int, ok bool) {
	if len(needle) == 0 || len(needle) > len(hay) {
		return 0, false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i + 1, true
		}
	}
	return 0, false
}

// CopySource looks for the origin of block, a run of lines that appeared in
// newPath at child. It searches the parent versions of the other files this
// commit changed for a contiguous occurrence of block, returning the source path
// and the 1-based line where the block starts there.
//
// A block with too little substance (< MinCopyChars non-whitespace characters)
// is never matched, so a trivial added line is not taken for moved code. When
// several changed files contain the block, the lexicographically smallest path
// wins, for determinism.
func (r *Repo) CopySource(child, parent *object.Commit, newPath string, block []string) (srcPath string, srcStart int, ok bool, err error) {
	if BlockSubstance(block) < MinCopyChars {
		return "", 0, false, nil
	}

	childTree, err := child.Tree()
	if err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: tree of %s: %w", child.Hash.String(), err)
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: tree of %s: %w", parent.Hash.String(), err)
	}

	// Blob hashes on the child side, to tell which parent files this commit
	// actually changed (modified or deleted).
	childHashes := map[string]plumbing.Hash{}
	if err := childTree.Files().ForEach(func(f *object.File) error {
		childHashes[f.Name] = f.Hash
		return nil
	}); err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: iterating child tree of %s: %w", child.Hash.String(), err)
	}

	// Candidate sources: parent files the commit changed, other than the target.
	var candidates []string
	if err := parentTree.Files().ForEach(func(f *object.File) error {
		if f.Name == newPath {
			return nil
		}
		if h, ok := childHashes[f.Name]; ok && h == f.Hash {
			return nil // unchanged in this commit
		}
		candidates = append(candidates, f.Name)
		return nil
	}); err != nil {
		return "", 0, false, fmt.Errorf("gitlayer: iterating parent tree of %s: %w", parent.Hash.String(), err)
	}
	sort.Strings(candidates)

	for _, name := range candidates {
		lines, ferr := r.FileAt(parent, name)
		if ferr != nil {
			return "", 0, false, ferr
		}
		if start, found := FindBlock(lines, block); found {
			return name, start, true, nil
		}
	}
	return "", 0, false, nil
}

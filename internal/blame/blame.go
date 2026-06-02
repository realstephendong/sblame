// Package blame implements the recursive line-range tracking engine for sblame.
//
// The core operation classifies how a line range changed between a commit and
// its parent, producing one of four outcomes:
//
//   - UNCHANGED: lines are byte-identical; continue walking to the parent.
//   - COSMETIC: lines differ only in whitespace/formatting; continue walking.
//   - MOVED: lines relocated to a different file or position; continue walking
//     with updated coordinates.
//   - AUTHORED: lines contain a genuine semantic change; stop and attribute
//     authorship to this commit.
package blame

import (
	"errors"

	"github.com/stephendong/sblame/internal/types"
)

// ErrNotImplemented is returned by stub functions that have no implementation yet.
var ErrNotImplemented = errors.New("blame: not implemented")

// StepResult holds the output of a single classification step. When
// Classification is AUTHORED, the engine should stop. For all other
// classifications, ParentCommit and NewRange describe where to continue.
type StepResult struct {
	// NewRange is the file/line-range in the parent commit's tree.
	// For MOVED, this may reference a different file path.
	NewRange types.LineRange

	// ParentCommit is the hash of the parent commit to continue into.
	ParentCommit string

	// Classification is the outcome of comparing the line range.
	Classification types.Classification
}

// ClassifyStep examines how the given line range changed between commit and
// its parent, returning a StepResult that tells the engine whether to stop
// (AUTHORED) or continue walking (UNCHANGED, COSMETIC, MOVED).
//
// This is a stub. The real implementation will diff the two commits, map the
// line range through the diff hunks, and apply heuristics to classify the change.
func ClassifyStep(filePath string, lr types.LineRange, commitHash string) (*StepResult, error) {
	return nil, ErrNotImplemented
}

// Run performs the full recursive blame walk starting from the given file,
// line range, and commit. It repeatedly calls ClassifyStep until an AUTHORED
// classification is found or history is exhausted.
//
// This is a stub.
func Run(filePath string, lr types.LineRange, commitHash string) (*types.BlameResult, error) {
	return nil, ErrNotImplemented
}

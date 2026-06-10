// Package blame implements the recursive line-range tracking engine for sblame.
//
// Starting from a line range in a commit, the engine walks first-parent history.
// At each step it classifies how the tracked range changed between a commit and
// its parent:
//
//   - UNCHANGED: the lines are identical in the parent; continue walking, with
//     the range remapped to the parent's line numbers.
//   - COSMETIC: the lines were touched but differ only in whitespace/formatting;
//     skip this commit and continue walking into the parent.
//   - AUTHORED: the lines contain a genuine (non-whitespace) change, or are newly
//     introduced here; stop and attribute authorship to this commit.
//   - MOVED: the lines relocated to a different file. Cross-file move detection
//     is not yet implemented — the diff layer is single-file — so the engine does
//     not currently produce MOVED. A within-file relocation surfaces as an
//     AUTHORED change at the commit that moved it. (Future work.)
package blame

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/realstephendong/sblame/internal/gitlayer"
	"github.com/realstephendong/sblame/internal/types"
)

// History is the subset of repository navigation the blame engine needs. It is
// declared here, at the point of use, so the engine can be exercised with a fake
// in tests; *gitlayer.Repo satisfies it.
type History interface {
	// Parent returns the commit to continue the walk into, or an error wrapping
	// gitlayer.ErrRootCommit when commit has no parents.
	Parent(commit *object.Commit) (*object.Commit, error)

	// FileAt returns the lines of path at commit, or an error wrapping
	// gitlayer.ErrFileNotFound when the path is absent there.
	FileAt(commit *object.Commit, path string) ([]string, error)
}

// StepResult holds the outcome of classifying one commit-to-parent step. When
// Classification is AUTHORED the walk stops; for UNCHANGED and COSMETIC, NewRange
// is the tracked range remapped into the parent's coordinates.
type StepResult struct {
	// Classification is how the tracked range changed at this commit.
	Classification types.Classification

	// NewRange is the range to continue tracking in the parent. It is only
	// meaningful for UNCHANGED and COSMETIC.
	NewRange types.LineRange

	// Confidence is a per-step multiplier in (0, 1] expressing how certain the
	// classification is. It is 1.0 for UNCHANGED and AUTHORED (exact, textual
	// decisions) and for whitespace-only COSMETIC skips. It drops below 1.0 for
	// a comment-only COSMETIC skip, because comment detection is heuristic; Run
	// multiplies these together so an attribution reached past fuzzier skips
	// reports lower overall confidence.
	Confidence float64
}

// confExact is the confidence multiplier for a step with no uncertainty: an
// UNCHANGED step, an AUTHORED stop, or a whitespace-only skip. Comment-only
// skips contribute less; see analyzeModification.
const confExact = 1.0

// Run performs the full blame walk: starting from start, it follows first-parent
// history, remapping the tracked range at each step, until it reaches the commit
// that authored the range (or the root commit, where the lines originate).
//
// The returned BlameResult attributes the range to that commit.
func Run(h History, start *object.Commit, lr types.LineRange) (*types.BlameResult, error) {
	// The range must exist in the starting commit before we begin walking.
	startLines, err := h.FileAt(start, lr.FilePath)
	if err != nil {
		return nil, err
	}
	if err := validateRange(lr, len(startLines)); err != nil {
		return nil, err
	}

	commit := start
	cur := lr
	conf := 1.0 // accumulated confidence, reduced by each fuzzy (comment) skip
	for {
		parent, err := h.Parent(commit)
		if errors.Is(err, gitlayer.ErrRootCommit) {
			// No parent to compare against: the lines originate here.
			return attribute(commit, conf), nil
		}
		if err != nil {
			return nil, err
		}

		step, err := ClassifyStep(h, commit, parent, cur)
		if err != nil {
			return nil, err
		}

		switch step.Classification {
		case types.AUTHORED:
			return attribute(commit, conf), nil
		case types.UNCHANGED, types.COSMETIC:
			conf *= step.Confidence
			commit = parent
			cur = step.NewRange
		default:
			return nil, fmt.Errorf("blame: unexpected classification %s", step.Classification)
		}
	}
}

// ClassifyStep examines how the tracked child range changed between child and
// its parent for a single file, returning whether to stop (AUTHORED) or continue
// (UNCHANGED/COSMETIC) and, if continuing, the range remapped into the parent.
func ClassifyStep(h History, child, parent *object.Commit, lr types.LineRange) (*StepResult, error) {
	parentLines, err := h.FileAt(parent, lr.FilePath)
	if err != nil {
		if errors.Is(err, gitlayer.ErrFileNotFound) {
			// The file is absent in the parent, so it — and the tracked lines —
			// first appeared at the child. Authored here.
			return &StepResult{Classification: types.AUTHORED, Confidence: confExact}, nil
		}
		return nil, err
	}
	childLines, err := h.FileAt(child, lr.FilePath)
	if err != nil {
		return nil, err
	}

	hunks := gitlayer.Diff(parentLines, childLines)
	overlap := overlappingHunks(hunks, lr.Start, lr.End)
	// An unknown extension yields the zero commentStyle, which the tokenizer
	// treats as the safe fallback (whitespace-only cosmetic detection).
	style, _ := styleForPath(lr.FilePath)

	hasAdded := false
	hasModified := false
	hasRealModification := false
	// stepConfidence is the weakest (lowest) confidence over the cosmetic hunks
	// touching the range; the step is only as certain as its least certain skip.
	stepConfidence := confExact
	for _, hk := range overlap {
		switch hk.Kind {
		case gitlayer.Added:
			hasAdded = true
		case gitlayer.Modified:
			hasModified = true
			cosmetic, confidence := analyzeModification(parentLines, childLines, hk, style)
			if !cosmetic {
				hasRealModification = true
			} else if confidence < stepConfidence {
				stepConfidence = confidence
			}
		}
	}

	// Pure additions and genuine modifications are authored at the child.
	if hasAdded || hasRealModification {
		return &StepResult{Classification: types.AUTHORED, Confidence: confExact}, nil
	}

	// Otherwise the range survives into the parent unchanged, or was only
	// reformatted/recommented (cosmetic). Either way, remap it and keep walking.
	span := mapRangeToParent(overlap, lr)
	class := types.UNCHANGED
	confidence := confExact
	if hasModified {
		class = types.COSMETIC
		confidence = stepConfidence
	}
	return &StepResult{
		Classification: class,
		NewRange: types.LineRange{
			FilePath: lr.FilePath,
			Start:    span.start,
			End:      span.end,
		},
		Confidence: confidence,
	}, nil
}

// overlappingHunks returns the hunks whose child-line span intersects the
// inclusive range [start, end]. Deleted hunks occupy no child lines and are
// skipped.
func overlappingHunks(hunks []gitlayer.Hunk, start, end int) []gitlayer.Hunk {
	var out []gitlayer.Hunk
	for _, hk := range hunks {
		if hk.ChildLen == 0 {
			continue
		}
		hkStart := hk.ChildStart
		hkEnd := hk.ChildStart + hk.ChildLen - 1
		if hkStart <= end && start <= hkEnd {
			out = append(out, hk)
		}
	}
	return out
}

// parentSpan is an inclusive 1-based line range in the parent.
type parentSpan struct {
	start, end int
}

// mapRangeToParent computes the parent line range the tracked child range
// corresponds to, as the bounding span over the overlapping hunks' parent lines.
// Equal hunks contribute the precise mapped intersection; cosmetic Modified hunks
// contribute their whole parent block (no 1:1 line correspondence exists).
//
// For a single tracked line — the common case — this is exact. For a multi-line
// range straddling deletions the bounding span can over-include intervening
// parent lines; that approximation is a property of the contiguous LineRange
// model and is acceptable for now.
func mapRangeToParent(overlap []gitlayer.Hunk, lr types.LineRange) parentSpan {
	var span parentSpan
	first := true
	for _, hk := range overlap {
		var ps, pe int
		switch hk.Kind {
		case gitlayer.Equal:
			cs := max(lr.Start, hk.ChildStart)
			ce := min(lr.End, hk.ChildStart+hk.ChildLen-1)
			ps = hk.ParentStart + (cs - hk.ChildStart)
			pe = hk.ParentStart + (ce - hk.ChildStart)
		case gitlayer.Modified:
			ps = hk.ParentStart
			pe = hk.ParentStart + hk.ParentLen - 1
		default:
			// Added/Deleted contribute no parent lines; on a continue path
			// (UNCHANGED/COSMETIC) Added never overlaps, so this is unreachable.
			continue
		}
		if first {
			span = parentSpan{start: ps, end: pe}
			first = false
		} else {
			span.start = min(span.start, ps)
			span.end = max(span.end, pe)
		}
	}
	if first {
		// Defensive: nothing contributed a parent line. Keep the range as-is.
		return parentSpan{start: lr.Start, end: lr.End}
	}
	return span
}

// sliceLines returns the lines [start, start+length) (1-based start) clamped to
// the slice bounds.
func sliceLines(lines []string, start, length int) []string {
	if length <= 0 {
		return nil
	}
	lo := start - 1
	hi := lo + length
	if lo < 0 {
		lo = 0
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	if lo >= hi {
		return nil
	}
	return lines[lo:hi]
}

// validateRange checks that lr is a sane 1-based range within an n-line file.
func validateRange(lr types.LineRange, n int) error {
	if lr.Start < 1 || lr.End < lr.Start {
		return fmt.Errorf("blame: invalid line range %d-%d", lr.Start, lr.End)
	}
	if lr.End > n {
		return fmt.Errorf("blame: line %d is out of range (%s has %d lines)", lr.End, lr.FilePath, n)
	}
	return nil
}

// attribute builds the final BlameResult for the commit the range is attributed
// to. confidence is the walk's accumulated certainty: 1.0 when authorship was
// reached only past exact (UNCHANGED) and whitespace-only steps, and lower when
// the walk skipped one or more comment-only changes.
func attribute(c *object.Commit, confidence float64) *types.BlameResult {
	return &types.BlameResult{
		Author:     c.Author.Name,
		Date:       c.Author.When,
		CommitHash: c.Hash.String(),
		Confidence: confidence,
	}
}

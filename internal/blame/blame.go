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
//
// File renames are followed: when the tracked file is absent in the parent, the
// engine asks the history layer for a rename source (a deleted file whose content
// is similar enough) and, if one is found, continues the walk under the old path
// — so a line's history survives a `git mv`. See gitlayer.RenameSource.
//
// Cross-file moves are followed too: when the tracked lines were added at a
// commit but a substantial block of them was moved or copied from another file
// the same commit changed, the step is classified MOVED and the walk continues in
// that source file — so authorship traces to where the code was really written.
// See gitlayer.CopySource. (Detecting copies from files the commit did NOT touch,
// git's `-C -C` / `-C -C -C`, is not yet implemented.)
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

	// RenameSource reports whether newPath in child was renamed from a different
	// path in parent. When it was, ok is true, oldPath is the pre-rename path,
	// and score is the content similarity in (0, 1] (1.0 for an exact rename).
	// When no rename source is found ok is false, and the caller treats newPath
	// as newly introduced at child. It is called only when newPath is absent in
	// parent. *gitlayer.Repo satisfies it via its own similarity matching.
	RenameSource(child, parent *object.Commit, newPath string) (oldPath string, score float64, ok bool, err error)

	// CopySource reports whether block — a run of lines added to newPath at child
	// — was moved or copied from another file the same commit changed. When it
	// was, ok is true, srcPath is that file and srcStart is the 1-based line where
	// the block begins in srcPath at parent. It is called only when the tracked
	// range is a pure addition; *gitlayer.Repo satisfies it via its own block
	// matching.
	CopySource(child, parent *object.Commit, newPath string, block []string) (srcPath string, srcStart int, ok bool, err error)
}

// StepResult holds the outcome of classifying one commit-to-parent step. When
// Classification is AUTHORED the walk stops; for UNCHANGED, COSMETIC, and MOVED,
// NewRange is the tracked range remapped into the parent's coordinates (a
// different file, for MOVED).
type StepResult struct {
	// Classification is how the tracked range changed at this commit.
	Classification types.Classification

	// NewRange is the range to continue tracking in the parent. It is only
	// meaningful for UNCHANGED, COSMETIC, and MOVED (where its FilePath is the
	// source file the lines moved from).
	NewRange types.LineRange

	// Confidence is a per-step multiplier in (0, 1] expressing how certain the
	// classification is. It is 1.0 for AUTHORED and for an UNCHANGED or
	// whitespace-only COSMETIC step that did not cross a fuzzy rename. It drops
	// below 1.0 for a comment-only COSMETIC skip, for a step that continues across
	// a similarity-matched file rename (an exact rename costs nothing), and for a
	// MOVED step that follows a moved/copied block to another file — all heuristic
	// decisions. Run multiplies these together so an attribution reached past
	// fuzzier skips reports lower overall confidence.
	Confidence float64
}

// confExact is the confidence multiplier for a step with no uncertainty: an
// UNCHANGED step, an AUTHORED stop, or a whitespace-only skip. Comment-only
// skips contribute less; see analyzeModification.
const confExact = 1.0

// fileRenamePenalty is the most a similarity-matched file rename can reduce
// confidence, reached as the match approaches the rename threshold. An exact
// rename (identical content, similarity 1.0) is certain and costs nothing. This
// is distinct from structrename.go's renamePenalty, which is about an identifier
// being renamed *within* a file; this is about the whole file being renamed.
const fileRenamePenalty = 0.2

// confForFileRename maps a rename similarity score to a confidence multiplier:
// 1.0 for an exact rename, falling linearly to confExact-fileRenamePenalty as the
// score approaches 0. Crossing a heuristically-matched rename is less certain
// than crossing one git could prove by hash.
func confForFileRename(score float64) float64 {
	return confExact - fileRenamePenalty*(1-score)
}

// copyPenalty is the confidence cost of following a move/copy of a line block
// into the source file it came from. The block matched another file's content
// exactly, but cross-file attribution is still a heuristic, so the step is not
// fully certain.
const copyPenalty = 0.1

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
		case types.UNCHANGED, types.COSMETIC, types.MOVED:
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
	// The parent side is normally the same path as the child. When the file is
	// absent in the parent it may have been renamed; if so, parentPath becomes
	// the old path and the walk continues there. renameConf is the cost of that
	// (heuristic) rename match: 1.0 for an exact rename, less for a fuzzy one.
	parentPath := lr.FilePath
	renameConf := confExact
	parentLines, err := h.FileAt(parent, parentPath)
	if err != nil {
		if !errors.Is(err, gitlayer.ErrFileNotFound) {
			return nil, err
		}
		// The path is absent in the parent: either the file was genuinely
		// introduced at the child, or it was renamed from another path. Ask the
		// history layer for a rename source.
		oldPath, score, ok, rerr := h.RenameSource(child, parent, lr.FilePath)
		if rerr != nil {
			return nil, rerr
		}
		if !ok {
			// No rename source: the lines first appeared at the child.
			return &StepResult{Classification: types.AUTHORED, Confidence: confExact}, nil
		}
		parentPath = oldPath
		renameConf = confForFileRename(score)
		parentLines, err = h.FileAt(parent, parentPath)
		if err != nil {
			return nil, err
		}
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
	// Rung 2: if the whole file change is a consistent rename, every modified
	// hunk in it is cosmetic. This is a property of the file pair, so it is
	// computed once here rather than per hunk. lexer is nil for languages with no
	// structural lexer, where rename detection is skipped.
	lexer := lexerForPath(lr.FilePath)
	renameStep := lexer != nil && detectRename(lexer, parentLines, childLines)

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
			if renameStep {
				ratio := renameRatio(lexer,
					sliceLines(parentLines, hk.ParentStart, hk.ParentLen),
					sliceLines(childLines, hk.ChildStart, hk.ChildLen))
				if conf := confExact - renamePenalty*ratio; conf < stepConfidence {
					stepConfidence = conf
				}
				continue // cosmetic rename: not a real modification
			}
			cosmetic, confidence := analyzeModification(parentLines, childLines, hk, style)
			if !cosmetic {
				hasRealModification = true
			} else if confidence < stepConfidence {
				stepConfidence = confidence
			}
		}
	}

	// A genuine modification is authored at the child outright.
	if hasRealModification {
		return &StepResult{Classification: types.AUTHORED, Confidence: confExact}, nil
	}

	// A pure addition is authored at the child — unless the added lines were
	// moved or copied from another file this commit changed, in which case the
	// walk continues in that source file (the lines were authored there). Only
	// the common single-Added-hunk case is handled; a range straddling several
	// hunks falls through to AUTHORED.
	if hasAdded {
		if len(overlap) == 1 && overlap[0].Kind == gitlayer.Added {
			hk := overlap[0]
			block := sliceLines(childLines, hk.ChildStart, hk.ChildLen)
			src, srcStart, ok, cerr := h.CopySource(child, parent, lr.FilePath, block)
			if cerr != nil {
				return nil, cerr
			}
			if ok {
				offset := lr.Start - hk.ChildStart
				return &StepResult{
					Classification: types.MOVED,
					NewRange: types.LineRange{
						FilePath: src,
						Start:    srcStart + offset,
						End:      srcStart + offset + (lr.End - lr.Start),
					},
					Confidence: confExact - copyPenalty,
				}, nil
			}
		}
		return &StepResult{Classification: types.AUTHORED, Confidence: confExact}, nil
	}

	// Otherwise the range survives into the parent unchanged, or was only
	// reformatted/recommented (cosmetic). Either way, remap it — into the parent's
	// path, which differs from the child's when we crossed a rename — and keep
	// walking. Confidence combines the cosmetic-skip certainty with the rename
	// match: an exact (or absent) rename leaves renameConf at 1.0, so this matches
	// the non-rename behavior.
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
			FilePath: parentPath,
			Start:    span.start,
			End:      span.end,
		},
		Confidence: confidence * renameConf,
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

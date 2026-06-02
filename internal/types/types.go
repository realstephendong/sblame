// Package types defines the core domain types for sblame.
package types

import "time"

// Classification describes how a line range changed between two commits.
// The blame engine uses this to decide whether to keep walking history
// or to stop and attribute authorship.
type Classification int

const (
	// UNCHANGED means the lines are byte-identical between child and parent.
	// The engine continues walking to the parent.
	UNCHANGED Classification = iota

	// COSMETIC means the lines differ only in whitespace, formatting,
	// renaming, or other non-semantic ways. The engine continues walking.
	COSMETIC

	// AUTHORED means the lines contain a genuine semantic change.
	// The engine stops and attributes authorship to this commit.
	AUTHORED

	// MOVED means the lines were relocated from another file or position
	// without semantic change. The engine continues walking with the
	// updated file/range coordinates.
	MOVED
)

// String returns the human-readable name of a Classification value.
func (c Classification) String() string {
	switch c {
	case UNCHANGED:
		return "UNCHANGED"
	case COSMETIC:
		return "COSMETIC"
	case AUTHORED:
		return "AUTHORED"
	case MOVED:
		return "MOVED"
	default:
		return "UNKNOWN"
	}
}

// Commit represents a git commit with the fields relevant to blame analysis.
type Commit struct {
	// Hash is the full SHA-1 hex string of the commit.
	Hash string

	// Author is the name of the commit author.
	Author string

	// Date is the author date of the commit.
	Date time.Time

	// Parents is the list of parent commit hashes.
	// A merge commit will have more than one parent.
	Parents []string

	// Message is the full commit message.
	Message string
}

// LineRange identifies a contiguous range of lines within a file.
type LineRange struct {
	// FilePath is the repository-relative path to the file.
	FilePath string

	// Start is the 1-based starting line number (inclusive).
	Start int

	// End is the 1-based ending line number (inclusive).
	End int
}

// BlameResult is the final attribution for a line range, produced when the
// engine encounters an AUTHORED classification.
type BlameResult struct {
	// Author is the name of the person who genuinely authored the logic.
	Author string

	// Date is the author date of the commit that introduced the logic.
	Date time.Time

	// CommitHash is the SHA-1 hex string of the authoring commit.
	CommitHash string

	// Confidence is a value between 0.0 and 1.0 indicating how certain
	// the engine is about this attribution. A value of 1.0 means the
	// change was unambiguously authored; lower values may arise from
	// heuristic classification of complex diffs.
	Confidence float64
}

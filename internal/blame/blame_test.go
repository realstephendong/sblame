package blame

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/realstephendong/sblame/internal/gitlayer"
	"github.com/realstephendong/sblame/internal/types"
)

const testPath = "file.go"

// mkCommit builds a minimal *object.Commit with a deterministic hash and author.
func mkCommit(n int, author string) *object.Commit {
	return &object.Commit{
		Hash:   plumbing.NewHash(fmt.Sprintf("%040x", n)),
		Author: object.Signature{Name: author, When: time.Unix(int64(n)*1000, 0)},
	}
}

// fakeHistory models a linear chain of commits over a single file. order[0] is
// the root and order[len-1] is the head. A commit absent from lines is treated
// as not containing the file (file-added boundary).
type fakeHistory struct {
	order []*object.Commit
	lines map[plumbing.Hash][]string
}

func (f *fakeHistory) Parent(c *object.Commit) (*object.Commit, error) {
	for i, cc := range f.order {
		if cc.Hash == c.Hash {
			if i == 0 {
				return nil, gitlayer.ErrRootCommit
			}
			return f.order[i-1], nil
		}
	}
	return nil, fmt.Errorf("fakeHistory: unknown commit %s", c.Hash)
}

func (f *fakeHistory) FileAt(c *object.Commit, _ string) ([]string, error) {
	lines, ok := f.lines[c.Hash]
	if !ok {
		return nil, fmt.Errorf("%w: %s", gitlayer.ErrFileNotFound, c.Hash)
	}
	return lines, nil
}

// build constructs a fakeHistory from ordered (author, lines) versions; a nil
// lines slice means the file does not exist at that commit.
func build(t *testing.T, versions []struct {
	author string
	lines  []string
}) (*fakeHistory, []*object.Commit) {
	t.Helper()
	f := &fakeHistory{lines: map[plumbing.Hash][]string{}}
	commits := make([]*object.Commit, len(versions))
	for i, v := range versions {
		c := mkCommit(i+1, v.author)
		commits[i] = c
		f.order = append(f.order, c)
		if v.lines != nil {
			f.lines[c.Hash] = v.lines
		}
	}
	return f, commits
}

func TestRun_SkipsCosmeticAndAttributesToAuthor(t *testing.T) {
	// c0 authors the line; c1 reindents it (cosmetic); c2 prepends a header
	// (shifts the line, but leaves it unchanged).
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"func foo() {", "  return 1", "}"}},
		{"bob", []string{"func foo() {", "      return 1", "}"}},
		{"carol", []string{"// header", "func foo() {", "      return 1", "}"}},
	})
	head := commits[2]

	// "return 1" is line 3 in the head commit.
	result, err := Run(f, head, types.LineRange{FilePath: testPath, Start: 3, End: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "alice" {
		t.Errorf("author: got %q, want %q (cosmetic reindent + shift should be skipped)", result.Author, "alice")
	}
	if result.CommitHash != commits[0].Hash.String() {
		t.Errorf("commit: got %s, want %s", result.CommitHash, commits[0].Hash)
	}
}

func TestRun_GenuineChangeIsAuthored(t *testing.T) {
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"return 1"}},
		{"bob", []string{"return 2"}}, // semantic change
	})

	result, err := Run(f, commits[1], types.LineRange{FilePath: testPath, Start: 1, End: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "bob" {
		t.Errorf("author: got %q, want %q", result.Author, "bob")
	}
}

func TestRun_FileAddedIsAuthored(t *testing.T) {
	// c0 (root) does not contain the file; c1 introduces it.
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", nil}, // file absent
		{"bob", []string{"x := 1"}},
	})

	result, err := Run(f, commits[1], types.LineRange{FilePath: testPath, Start: 1, End: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "bob" {
		t.Errorf("author: got %q, want %q", result.Author, "bob")
	}
}

func TestRun_PureAdditionIsAuthored(t *testing.T) {
	// A new line inserted between existing lines is authored at the inserting commit.
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"a", "b"}},
		{"bob", []string{"a", "NEW", "b"}},
	})

	result, err := Run(f, commits[1], types.LineRange{FilePath: testPath, Start: 2, End: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "bob" {
		t.Errorf("author: got %q, want %q", result.Author, "bob")
	}
}

func TestRun_SingleRootCommit(t *testing.T) {
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"only"}},
	})

	result, err := Run(f, commits[0], types.LineRange{FilePath: testPath, Start: 1, End: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "alice" {
		t.Errorf("author: got %q, want %q", result.Author, "alice")
	}
}

func TestRun_OutOfRange(t *testing.T) {
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"a", "b"}},
	})

	_, err := Run(f, commits[0], types.LineRange{FilePath: testPath, Start: 99, End: 99})
	if err == nil {
		t.Fatal("expected an out-of-range error, got nil")
	}
}

func TestRun_MissingFileInStartCommit(t *testing.T) {
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", nil}, // file does not exist at the start commit
	})

	_, err := Run(f, commits[0], types.LineRange{FilePath: testPath, Start: 1, End: 1})
	if !errors.Is(err, gitlayer.ErrFileNotFound) {
		t.Errorf("got %v, want an error wrapping ErrFileNotFound", err)
	}
}

func TestRun_SkipsCommentOnlyChangeWithReducedConfidence(t *testing.T) {
	// alice writes the line; bob only rewords its trailing comment. The line is
	// attributed to alice, but confidence drops below 1.0 because skipping a
	// comment-only change relies on heuristic comment detection.
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"func f() {", "\treturn 1 // base case", "}"}},
		{"bob", []string{"func f() {", "\treturn 1 // handles the empty input", "}"}},
	})
	head := commits[1]

	result, err := Run(f, head, types.LineRange{FilePath: testPath, Start: 2, End: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "alice" {
		t.Errorf("author: got %q, want %q (comment-only change should be skipped)", result.Author, "alice")
	}
	if result.Confidence >= 1.0 {
		t.Errorf("confidence: got %v, want < 1.0 for a comment-only skip", result.Confidence)
	}
}

func TestRun_WhitespaceSkipKeepsFullConfidence(t *testing.T) {
	// A whitespace-only reformat is a safe skip, so confidence stays at 1.0.
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"return 1"}},
		{"bob", []string{"  return   1"}},
	})

	result, err := Run(f, commits[1], types.LineRange{FilePath: testPath, Start: 1, End: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "alice" {
		t.Errorf("author: got %q, want %q", result.Author, "alice")
	}
	if result.Confidence != 1.0 {
		t.Errorf("confidence: got %v, want 1.0 for a whitespace-only skip", result.Confidence)
	}
}

func TestClassifyStep(t *testing.T) {
	parent := mkCommit(1, "alice")
	child := mkCommit(2, "bob")

	tests := []struct {
		name       string
		parentLine []string
		childLine  []string
		fileAbsent bool // file absent in parent
		lr         types.LineRange
		wantClass  types.Classification
		wantRange  types.LineRange // checked when continuing
	}{
		{
			name:       "unchanged line maps to shifted parent line",
			parentLine: []string{"a", "b", "c"},
			childLine:  []string{"header", "a", "b", "c"},
			lr:         types.LineRange{FilePath: testPath, Start: 3, End: 3}, // "b"
			wantClass:  types.UNCHANGED,
			wantRange:  types.LineRange{FilePath: testPath, Start: 2, End: 2}, // "b" at parent line 2
		},
		{
			name:       "whitespace-only change is cosmetic",
			parentLine: []string{"x", "return 1", "y"},
			childLine:  []string{"x", "    return  1", "y"},
			lr:         types.LineRange{FilePath: testPath, Start: 2, End: 2},
			wantClass:  types.COSMETIC,
			wantRange:  types.LineRange{FilePath: testPath, Start: 2, End: 2},
		},
		{
			name:       "real modification is authored",
			parentLine: []string{"x", "return 1", "y"},
			childLine:  []string{"x", "return 2", "y"},
			lr:         types.LineRange{FilePath: testPath, Start: 2, End: 2},
			wantClass:  types.AUTHORED,
		},
		{
			name:       "added line in range is authored",
			parentLine: []string{"a", "b"},
			childLine:  []string{"a", "NEW", "b"},
			lr:         types.LineRange{FilePath: testPath, Start: 2, End: 2},
			wantClass:  types.AUTHORED,
		},
		{
			name:       "file absent in parent is authored",
			fileAbsent: true,
			childLine:  []string{"a"},
			lr:         types.LineRange{FilePath: testPath, Start: 1, End: 1},
			wantClass:  types.AUTHORED,
		},
		{
			name:       "comment-only change is cosmetic",
			parentLine: []string{"x", "y := 1 // old", "z"},
			childLine:  []string{"x", "y := 1 // a clearer note", "z"},
			lr:         types.LineRange{FilePath: testPath, Start: 2, End: 2},
			wantClass:  types.COSMETIC,
			wantRange:  types.LineRange{FilePath: testPath, Start: 2, End: 2},
		},
		{
			name:       "code change alongside comment is authored",
			parentLine: []string{"y := 1 // note"},
			childLine:  []string{"y := 2 // note"},
			lr:         types.LineRange{FilePath: testPath, Start: 1, End: 1},
			wantClass:  types.AUTHORED,
		},
		{
			// The "//" is inside a string literal, not a comment, so the change
			// to the URL is a genuine code change — not a cosmetic comment edit.
			name:       "comment delimiter inside a string is not a comment",
			parentLine: []string{`u := "http://a"`},
			childLine:  []string{`u := "http://b"`},
			lr:         types.LineRange{FilePath: testPath, Start: 1, End: 1},
			wantClass:  types.AUTHORED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeHistory{lines: map[plumbing.Hash][]string{child.Hash: tt.childLine}}
			if !tt.fileAbsent {
				f.lines[parent.Hash] = tt.parentLine
			}

			step, err := ClassifyStep(f, child, parent, tt.lr)
			if err != nil {
				t.Fatalf("ClassifyStep: %v", err)
			}
			if step.Classification != tt.wantClass {
				t.Fatalf("classification: got %s, want %s", step.Classification, tt.wantClass)
			}
			if tt.wantClass == types.UNCHANGED || tt.wantClass == types.COSMETIC {
				if step.NewRange != tt.wantRange {
					t.Errorf("new range: got %+v, want %+v", step.NewRange, tt.wantRange)
				}
			}
		})
	}
}

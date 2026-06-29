// Package eval provides an evaluation harness for measuring sblame accuracy
// against known-good blame attributions.
//
// Each Case is a small, hand-constructed linear history with a known-correct
// answer: which author genuinely wrote a given line, and a floor on the
// confidence the engine should report. RunCases executes the real blame engine
// (including its diff and cosmetic/comment classification) over an in-memory
// history and checks the result, so the harness doubles as a regression gate
// for the semantic heuristics — see eval_test.go.
//
// The histories are synthetic rather than real git repositories: the engine
// reaches the repository only through the small blame.History interface, so an
// in-memory implementation exercises every classification path without the cost
// and nondeterminism of building real commits. Evaluating against real-world
// repositories with curated ground truth is future work.
package eval

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/realstephendong/sblame/internal/blame"
	"github.com/realstephendong/sblame/internal/gitlayer"
	"github.com/realstephendong/sblame/internal/types"
)

// version is one commit's state of the tracked file. A nil lines slice means
// the file does not exist at that commit (a file-add boundary).
type version struct {
	author string
	lines  []string
	// path is the file's path at this commit; empty means the Case's path. Set
	// it on a renamed file's pre-rename versions so the walk has a rename to
	// follow. It is the last field so existing positional literals stay valid.
	path string
	// extra holds additional files present at this commit, beyond path:lines, so
	// one commit can touch more than one file (e.g. a move's source and target).
	extra map[string][]string
}

// Case is one evaluation scenario: a linear history (oldest first), a line to
// blame in the head commit, and the expected attribution.
type Case struct {
	name       string
	path       string
	history    []version
	queryLine  int
	wantAuthor string
	minConf    float64 // the lowest acceptable reported confidence
}

// Result is the outcome of evaluating one Case.
type Result struct {
	Name   string
	Pass   bool
	Detail string
}

// Report aggregates the results of a RunCases invocation.
type Report struct {
	Results []Result
}

// Passed returns the number of passing cases.
func (r Report) Passed() int {
	n := 0
	for _, res := range r.Results {
		if res.Pass {
			n++
		}
	}
	return n
}

// Accuracy returns the fraction of cases that passed, or 0 when there are none.
func (r Report) Accuracy() float64 {
	if len(r.Results) == 0 {
		return 0
	}
	return float64(r.Passed()) / float64(len(r.Results))
}

// String renders a human-readable summary.
func (r Report) String() string {
	var b strings.Builder
	for _, res := range r.Results {
		mark := "FAIL"
		if res.Pass {
			mark = "ok  "
		}
		fmt.Fprintf(&b, "  [%s] %-40s %s\n", mark, res.Name, res.Detail)
	}
	fmt.Fprintf(&b, "  %d/%d passed (%.0f%% accuracy)\n", r.Passed(), len(r.Results), r.Accuracy()*100)
	return b.String()
}

// Run executes the built-in evaluation suite, prints a report, and returns a
// non-nil error if any case failed.
func Run() error {
	rep := RunCases(builtinCases())
	fmt.Print(rep.String())
	if rep.Passed() != len(rep.Results) {
		return fmt.Errorf("eval: %d/%d cases passed", rep.Passed(), len(rep.Results))
	}
	return nil
}

// RunCases evaluates each case against the real blame engine and returns a
// Report. A case passes when the engine errs out neither, attributes the line
// to the expected author, and reports at least the case's minimum confidence.
func RunCases(cases []Case) Report {
	var rep Report
	for _, c := range cases {
		h, head := buildHistory(c)
		res, err := blame.Run(h, head, types.LineRange{FilePath: c.path, Start: c.queryLine, End: c.queryLine})

		r := Result{Name: c.name}
		switch {
		case err != nil:
			r.Detail = fmt.Sprintf("error: %v", err)
		case res.Author != c.wantAuthor:
			r.Detail = fmt.Sprintf("author = %q, want %q", res.Author, c.wantAuthor)
		case res.Confidence < c.minConf:
			r.Detail = fmt.Sprintf("confidence = %.2f, want >= %.2f", res.Confidence, c.minConf)
		default:
			r.Pass = true
			r.Detail = fmt.Sprintf("%s @ %.0f%%", res.Author, res.Confidence*100)
		}
		rep.Results = append(rep.Results, r)
	}
	return rep
}

// builtinCases is the curated suite: one scenario per classification path the
// engine must get right.
func builtinCases() []Case {
	return []Case{
		{
			name:       "whitespace-only reformat is skipped",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"return 1"}}, {author: "bob", lines: []string{"   return  1"}}},
			queryLine:  1,
			wantAuthor: "alice",
			minConf:    1.0,
		},
		{
			name:       "comment-only edit is skipped",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"func f() {", "\treturn 1 // base", "}"}}, {author: "bob", lines: []string{"func f() {", "\treturn 1 // reworded for clarity", "}"}}},
			queryLine:  2,
			wantAuthor: "alice",
			minConf:    0.9,
		},
		{
			// A one-word comment tweak should dent confidence far less than a
			// full reword, since most of the comment is unchanged.
			name:       "small comment tweak keeps high confidence",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"x := 1 // returns the running total"}}, {author: "bob", lines: []string{"x := 1 // returns the running sum"}}},
			queryLine:  1,
			wantAuthor: "alice",
			minConf:    0.95,
		},
		{
			name:       "// inside a string is a code change, not a comment",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{`u := "http://a"`}}, {author: "bob", lines: []string{`u := "http://b"`}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
		{
			name:       "genuine code change is authored",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"return 1"}}, {author: "bob", lines: []string{"return 2"}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
		{
			// Rung 2: a consistently renamed identifier is skipped to the author.
			name:       "consistent rename is skipped",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"sum := total + total"}}, {author: "bob", lines: []string{"sum := amount + amount"}}},
			queryLine:  1,
			wantAuthor: "alice",
			minConf:    0.8,
		},
		{
			// Rung 2 cross-language: the same rename skip via tree-sitter, on a
			// non-Go file (Python). data -> info is consistent and recurs, so the
			// line stays with its author.
			name:       "consistent rename is skipped (python)",
			path:       "x.py",
			history:    []version{{author: "alice", lines: []string{"def run(x):", "    return data + data"}}, {author: "bob", lines: []string{"def run(x):", "    return info + info"}}},
			queryLine:  2,
			wantAuthor: "alice",
			minConf:    0.8,
		},
		{
			// Rung 2 safety: a one-off symbol swap is a real change, not a rename.
			name:       "single-occurrence swap is authored",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"x := foo()"}}, {author: "bob", lines: []string{"x := bar()"}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
		{
			name:       "reindent then header shift still finds origin",
			path:       "x.go",
			history:    []version{{author: "alice", lines: []string{"func foo() {", "  return 1", "}"}}, {author: "bob", lines: []string{"func foo() {", "      return 1", "}"}}, {author: "carol", lines: []string{"// header", "func foo() {", "      return 1", "}"}}},
			queryLine:  4,
			wantAuthor: "alice",
			minConf:    1.0,
		},
		{
			name:       "newly introduced file is authored",
			path:       "x.go",
			history:    []version{{author: "alice", lines: nil}, {author: "bob", lines: []string{"x := 1"}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
		{
			// Part 1: an exact file rename (no content change) is followed across
			// the rename, so the line stays with its original author at full
			// confidence — a rename commit must not steal authorship.
			name: "exact file rename is followed",
			path: "new.go",
			history: []version{
				{author: "alice", path: "old.go", lines: []string{"x := compute()"}},
				{author: "bob", lines: []string{"x := compute()"}}, // renamed, no edit
			},
			queryLine:  1,
			wantAuthor: "alice",
			minConf:    1.0,
		},
		{
			// Part 1: a rename detected by similarity (the commit also edited an
			// unrelated line) is still followed, but the surviving line is
			// attributed at reduced confidence because the match is heuristic.
			name: "similar file rename is followed at reduced confidence",
			path: "renamed.go",
			history: []version{
				{author: "alice", path: "orig.go", lines: []string{"a := 1", "b := 2", "c := 3"}},
				{author: "bob", lines: []string{"a := 1", "b := 2", "c := 99"}}, // renamed + edited line 3
			},
			queryLine:  1, // "a := 1", unchanged across the rename
			wantAuthor: "alice",
			minConf:    0.8,
		},
		{
			// Part 2: a function moved from one file to another in a single
			// commit is traced to the author who wrote it in the source file,
			// not the commit that relocated it, at reduced confidence.
			name: "moved code is traced to the source file",
			path: "dst.go",
			history: []version{
				{
					author: "alice",
					path:   "src.go",
					lines: []string{
						"package compute",
						"",
						"func Checksum(data []byte) uint32 {",
						"\tvar sum uint32",
						"\tfor _, b := range data {",
						"\t\tsum += uint32(b)",
						"\t}",
						"\treturn sum",
						"}",
					},
					extra: map[string][]string{"dst.go": {"package util", ""}},
				},
				{
					author: "bob", // moves Checksum from src.go into dst.go
					lines: []string{
						"package util",
						"",
						"func Checksum(data []byte) uint32 {",
						"\tvar sum uint32",
						"\tfor _, b := range data {",
						"\t\tsum += uint32(b)",
						"\t}",
						"\treturn sum",
						"}",
					},
					extra: map[string][]string{"src.go": {"package compute", ""}},
				},
			},
			queryLine:  3, // "func Checksum(...)" — moved from src.go
			wantAuthor: "alice",
			minConf:    0.8,
		},
	}
}

// linearHistory is an in-memory blame.History over a linear commit chain.
// order[0] is the root; order[len-1] is the head. Each commit carries a small
// file tree (path -> lines) so cases can model file renames.
type linearHistory struct {
	order []*object.Commit
	files map[plumbing.Hash]map[string][]string
}

func (h *linearHistory) Parent(c *object.Commit) (*object.Commit, error) {
	for i, cc := range h.order {
		if cc.Hash == c.Hash {
			if i == 0 {
				return nil, gitlayer.ErrRootCommit
			}
			return h.order[i-1], nil
		}
	}
	return nil, fmt.Errorf("eval: unknown commit %s", c.Hash)
}

func (h *linearHistory) FileAt(c *object.Commit, path string) ([]string, error) {
	lines, ok := h.files[c.Hash][path]
	if !ok {
		return nil, fmt.Errorf("%w: %s @ %s", gitlayer.ErrFileNotFound, path, c.Hash)
	}
	return lines, nil
}

// RenameSource finds, among the files present in parent but absent in child, the
// one most similar to newPath — the in-memory analogue of gitlayer.RenameSource,
// reusing the same matching so eval exercises the real cross-rename walk.
func (h *linearHistory) RenameSource(child, parent *object.Commit, newPath string) (string, float64, bool, error) {
	childFiles := h.files[child.Hash]
	newLines, ok := childFiles[newPath]
	if !ok {
		return "", 0, false, nil
	}
	candidates := map[string][]string{}
	for path, lines := range h.files[parent.Hash] {
		if _, inChild := childFiles[path]; !inChild {
			candidates[path] = lines
		}
	}
	oldPath, score, found := gitlayer.BestRenameMatch(newLines, candidates, gitlayer.RenameThreshold)
	return oldPath, score, found, nil
}

// CopySource searches the parent files this commit changed (other than newPath)
// for block, mirroring gitlayer.CopySource over the in-memory trees.
func (h *linearHistory) CopySource(child, parent *object.Commit, newPath string, block []string) (string, int, bool, error) {
	if gitlayer.BlockSubstance(block) < gitlayer.MinCopyChars {
		return "", 0, false, nil
	}
	childFiles := h.files[child.Hash]
	parentFiles := h.files[parent.Hash]
	var names []string
	for path := range parentFiles {
		if path == newPath {
			continue
		}
		if cl, ok := childFiles[path]; ok && linesEqual(cl, parentFiles[path]) {
			continue // unchanged in this commit
		}
		names = append(names, path)
	}
	sort.Strings(names)
	for _, path := range names {
		if start, ok := gitlayer.FindBlock(parentFiles[path], block); ok {
			return path, start, true, nil
		}
	}
	return "", 0, false, nil
}

// linesEqual reports whether two line slices are identical.
func linesEqual(a, b []string) bool {
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

// buildHistory materializes a Case's versions into a linearHistory and returns
// it with the head commit.
func buildHistory(c Case) (*linearHistory, *object.Commit) {
	h := &linearHistory{files: map[plumbing.Hash]map[string][]string{}}
	var head *object.Commit
	for i, v := range c.history {
		commit := &object.Commit{
			Hash:   plumbing.NewHash(fmt.Sprintf("%040x", i+1)),
			Author: object.Signature{Name: v.author, When: time.Unix(int64(i+1)*1000, 0)},
		}
		h.order = append(h.order, commit)
		fm := map[string][]string{}
		if v.lines != nil {
			path := v.path
			if path == "" {
				path = c.path
			}
			fm[path] = v.lines
		}
		for p, lines := range v.extra {
			fm[p] = lines
		}
		if len(fm) > 0 {
			h.files[commit.Hash] = fm
		}
		head = commit
	}
	return h, head
}

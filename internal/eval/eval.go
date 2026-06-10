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
			history:    []version{{"alice", []string{"return 1"}}, {"bob", []string{"   return  1"}}},
			queryLine:  1,
			wantAuthor: "alice",
			minConf:    1.0,
		},
		{
			name:       "comment-only edit is skipped",
			path:       "x.go",
			history:    []version{{"alice", []string{"func f() {", "\treturn 1 // base", "}"}}, {"bob", []string{"func f() {", "\treturn 1 // reworded for clarity", "}"}}},
			queryLine:  2,
			wantAuthor: "alice",
			minConf:    0.9,
		},
		{
			// A one-word comment tweak should dent confidence far less than a
			// full reword, since most of the comment is unchanged.
			name:       "small comment tweak keeps high confidence",
			path:       "x.go",
			history:    []version{{"alice", []string{"x := 1 // returns the running total"}}, {"bob", []string{"x := 1 // returns the running sum"}}},
			queryLine:  1,
			wantAuthor: "alice",
			minConf:    0.95,
		},
		{
			name:       "// inside a string is a code change, not a comment",
			path:       "x.go",
			history:    []version{{"alice", []string{`u := "http://a"`}}, {"bob", []string{`u := "http://b"`}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
		{
			name:       "genuine code change is authored",
			path:       "x.go",
			history:    []version{{"alice", []string{"return 1"}}, {"bob", []string{"return 2"}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
		{
			name:       "reindent then header shift still finds origin",
			path:       "x.go",
			history:    []version{{"alice", []string{"func foo() {", "  return 1", "}"}}, {"bob", []string{"func foo() {", "      return 1", "}"}}, {"carol", []string{"// header", "func foo() {", "      return 1", "}"}}},
			queryLine:  4,
			wantAuthor: "alice",
			minConf:    1.0,
		},
		{
			name:       "newly introduced file is authored",
			path:       "x.go",
			history:    []version{{"alice", nil}, {"bob", []string{"x := 1"}}},
			queryLine:  1,
			wantAuthor: "bob",
			minConf:    1.0,
		},
	}
}

// linearHistory is an in-memory blame.History over a single file's linear
// commit chain. order[0] is the root; order[len-1] is the head.
type linearHistory struct {
	order []*object.Commit
	lines map[plumbing.Hash][]string
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

func (h *linearHistory) FileAt(c *object.Commit, _ string) ([]string, error) {
	lines, ok := h.lines[c.Hash]
	if !ok {
		return nil, fmt.Errorf("%w: %s", gitlayer.ErrFileNotFound, c.Hash)
	}
	return lines, nil
}

// buildHistory materializes a Case's versions into a linearHistory and returns
// it with the head commit.
func buildHistory(c Case) (*linearHistory, *object.Commit) {
	h := &linearHistory{lines: map[plumbing.Hash][]string{}}
	var head *object.Commit
	for i, v := range c.history {
		commit := &object.Commit{
			Hash:   plumbing.NewHash(fmt.Sprintf("%040x", i+1)),
			Author: object.Signature{Name: v.author, When: time.Unix(int64(i+1)*1000, 0)},
		}
		h.order = append(h.order, commit)
		if v.lines != nil {
			h.lines[commit.Hash] = v.lines
		}
		head = commit
	}
	return h, head
}

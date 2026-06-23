package gitlayer

import (
	"errors"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// newInMemoryRepo creates an empty in-memory git repository wrapped in a Repo.
// Tests are white-box (package gitlayer) so they can construct Repo directly.
func newInMemoryRepo(t *testing.T) (*Repo, billy.Filesystem, *git.Worktree) {
	t.Helper()
	fs := memfs.New()
	repo, err := git.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("git.Init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	return &Repo{repo: repo}, fs, wt
}

func sig() *object.Signature {
	return &object.Signature{
		Name:  "Test Author",
		Email: "test@example.com",
		When:  time.Now(),
	}
}

// commitFile writes path with content into the worktree and commits it, letting
// go-git chain the parent to current HEAD. Returns the new commit hash.
func commitFile(t *testing.T, fs billy.Filesystem, wt *git.Worktree, path, content, msg string) plumbing.Hash {
	t.Helper()
	f, err := fs.Create(path)
	if err != nil {
		t.Fatalf("fs.Create(%s): %v", path, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("write(%s): %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close(%s): %v", path, err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatalf("wt.Add(%s): %v", path, err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig(), AllowEmptyCommits: true})
	if err != nil {
		t.Fatalf("wt.Commit(%s): %v", msg, err)
	}
	return h
}

func TestCommitByHash_RoundTripAndNotFound(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)
	h := commitFile(t, fs, wt, "file.txt", "v1\n", "first")

	t.Run("resolves and round-trips the hash at the boundary", func(t *testing.T) {
		// Pass the hash in as a string (the API boundary) and confirm the
		// resolved commit's hash stringifies back to the same value.
		c, err := r.CommitByHash(h.String())
		if err != nil {
			t.Fatalf("CommitByHash: %v", err)
		}
		if got := c.Hash.String(); got != h.String() {
			t.Errorf("hash round-trip mismatch: got %s, want %s", got, h.String())
		}
	})

	t.Run("missing commit returns ErrCommitNotFound", func(t *testing.T) {
		_, err := r.CommitByHash("0123456789012345678901234567890123456789")
		if !errors.Is(err, ErrCommitNotFound) {
			t.Errorf("got %v, want ErrCommitNotFound", err)
		}
	})

	t.Run("malformed hash returns ErrCommitNotFound", func(t *testing.T) {
		_, err := r.CommitByHash("not-a-real-hash")
		if !errors.Is(err, ErrCommitNotFound) {
			t.Errorf("got %v, want ErrCommitNotFound", err)
		}
	})
}

func TestParent_WalkLinearChainToRoot(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)
	// Build a linear chain: c1 (root) <- c2 <- c3.
	c1 := commitFile(t, fs, wt, "file.txt", "v1\n", "c1")
	c2 := commitFile(t, fs, wt, "file.txt", "v1\nv2\n", "c2")
	c3 := commitFile(t, fs, wt, "file.txt", "v1\nv2\nv3\n", "c3")

	// Walk parents from the tip and collect the hashes we pass through.
	want := []plumbing.Hash{c3, c2, c1}
	commit, err := r.CommitByHash(c3.String())
	if err != nil {
		t.Fatalf("CommitByHash(c3): %v", err)
	}

	var got []plumbing.Hash
	for {
		got = append(got, commit.Hash)
		parent, err := r.Parent(commit)
		if errors.Is(err, ErrRootCommit) {
			break
		}
		if err != nil {
			t.Fatalf("Parent: %v", err)
		}
		commit = parent
	}

	if len(got) != len(want) {
		t.Fatalf("walked %d commits, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d: got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestParent_RootReturnsErrRootCommit(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)
	root := commitFile(t, fs, wt, "file.txt", "v1\n", "root")

	commit, err := r.CommitByHash(root.String())
	if err != nil {
		t.Fatalf("CommitByHash: %v", err)
	}
	if _, err := r.Parent(commit); !errors.Is(err, ErrRootCommit) {
		t.Errorf("got %v, want ErrRootCommit", err)
	}
}

func TestParent_MergeFollowsFirstParent(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)
	// Two ancestors; the second is chained after the first via HEAD.
	first := commitFile(t, fs, wt, "file.txt", "a\n", "first")
	second := commitFile(t, fs, wt, "other.txt", "b\n", "second")

	// Craft a merge commit with two explicit parents [first, second]. We only
	// assert that Parent follows the FIRST parent (Month 1 behavior).
	mergeHash, err := wt.Commit("merge", &git.CommitOptions{
		Author:            sig(),
		Parents:           []plumbing.Hash{first, second},
		AllowEmptyCommits: true,
	})
	if err != nil {
		t.Fatalf("merge commit: %v", err)
	}

	merge, err := r.CommitByHash(mergeHash.String())
	if err != nil {
		t.Fatalf("CommitByHash(merge): %v", err)
	}
	if merge.NumParents() != 2 {
		t.Fatalf("expected a merge commit with 2 parents, got %d", merge.NumParents())
	}

	parent, err := r.Parent(merge)
	if err != nil {
		t.Fatalf("Parent(merge): %v", err)
	}
	if parent.Hash != first {
		t.Errorf("merge parent: got %s, want first parent %s", parent.Hash, first)
	}
}

func TestFileAt(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)
	h := commitFile(t, fs, wt, "file.txt", "line1\nline2\nline3\n", "add file")
	commit, err := r.CommitByHash(h.String())
	if err != nil {
		t.Fatalf("CommitByHash: %v", err)
	}

	t.Run("existing file returns its lines", func(t *testing.T) {
		lines, err := r.FileAt(commit, "file.txt")
		if err != nil {
			t.Fatalf("FileAt: %v", err)
		}
		want := []string{"line1", "line2", "line3"}
		if len(lines) != len(want) {
			t.Fatalf("got %d lines %q, want %d", len(lines), lines, len(want))
		}
		for i := range want {
			if lines[i] != want[i] {
				t.Errorf("line %d: got %q, want %q", i+1, lines[i], want[i])
			}
		}
	})

	t.Run("missing file returns ErrFileNotFound", func(t *testing.T) {
		_, err := r.FileAt(commit, "does-not-exist.txt")
		if !errors.Is(err, ErrFileNotFound) {
			t.Errorf("got %v, want ErrFileNotFound", err)
		}
	})
}

func TestSplitLines_TrailingNewlineContract(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty file is zero lines", "", []string{}},
		{"trailing newline not an extra line", "a\nb\n", []string{"a", "b"}},
		{"no trailing newline", "a\nb", []string{"a", "b"}},
		{"single newline is one empty line", "\n", []string{""}},
		{"single line with newline", "a\n", []string{"a"}},
		{"blank line between content", "a\n\nb\n", []string{"a", "", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitLines(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d lines %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("line %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// renameCommit writes newPath with content, removes oldPath, and commits — the
// result looks like a `git mv` (with an edit too when content differs from the
// original).
func renameCommit(t *testing.T, fs billy.Filesystem, wt *git.Worktree, oldPath, newPath, content, msg string) plumbing.Hash {
	t.Helper()
	f, err := fs.Create(newPath)
	if err != nil {
		t.Fatalf("fs.Create(%s): %v", newPath, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("write(%s): %v", newPath, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close(%s): %v", newPath, err)
	}
	if _, err := wt.Add(newPath); err != nil {
		t.Fatalf("wt.Add(%s): %v", newPath, err)
	}
	if _, err := wt.Remove(oldPath); err != nil {
		t.Fatalf("wt.Remove(%s): %v", oldPath, err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig()})
	if err != nil {
		t.Fatalf("wt.Commit(%s): %v", msg, err)
	}
	return h
}

// mustResolve loads a commit by hash, failing the test on error.
func mustResolve(t *testing.T, r *Repo, h plumbing.Hash) *object.Commit {
	t.Helper()
	c, err := r.CommitByHash(h.String())
	if err != nil {
		t.Fatalf("CommitByHash(%s): %v", h, err)
	}
	return c
}

func TestRenameSource(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)
	const content = "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	c1 := commitFile(t, fs, wt, "math.go", content, "add math.go")
	c2 := renameCommit(t, fs, wt, "math.go", "calc.go", content, "rename math.go -> calc.go")
	const edited = "package main\n\nfunc Add(a, b int) int {\n\treturn a + b + 0\n}\n"
	c3 := renameCommit(t, fs, wt, "calc.go", "arith.go", edited, "rename calc.go -> arith.go with edit")

	t.Run("exact rename returns the old path at score 1.0", func(t *testing.T) {
		old, score, ok, err := r.RenameSource(mustResolve(t, r, c2), mustResolve(t, r, c1), "calc.go")
		if err != nil {
			t.Fatalf("RenameSource: %v", err)
		}
		if !ok || old != "math.go" {
			t.Fatalf("got (%q, %v, %v), want math.go", old, score, ok)
		}
		if score != 1.0 {
			t.Errorf("score = %v, want 1.0 for an exact rename", score)
		}
	})

	t.Run("similar rename returns the old path above threshold", func(t *testing.T) {
		old, score, ok, err := r.RenameSource(mustResolve(t, r, c3), mustResolve(t, r, c2), "arith.go")
		if err != nil {
			t.Fatalf("RenameSource: %v", err)
		}
		if !ok || old != "calc.go" {
			t.Fatalf("got (%q, %v, %v), want calc.go", old, score, ok)
		}
		if score < RenameThreshold || score >= 1.0 {
			t.Errorf("score = %v, want in [%v, 1.0)", score, RenameThreshold)
		}
	})

	t.Run("a genuinely new file has no rename source", func(t *testing.T) {
		c4 := commitFile(t, fs, wt, "unrelated.go", "package main\n", "add unrelated.go")
		_, _, ok, err := r.RenameSource(mustResolve(t, r, c4), mustResolve(t, r, c3), "unrelated.go")
		if err != nil {
			t.Fatalf("RenameSource: %v", err)
		}
		if ok {
			t.Error("expected no rename source for a brand-new file")
		}
	})
}

package gitlayer

import (
	"errors"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// writeFile writes content to path in the worktree filesystem (no staging).
func writeFile(t *testing.T, fs billy.Filesystem, path, content string) {
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
}

// addPath stages a single path.
func addPath(t *testing.T, wt *git.Worktree, path string) {
	t.Helper()
	if _, err := wt.Add(path); err != nil {
		t.Fatalf("wt.Add(%s): %v", path, err)
	}
}

// commitWT commits the current index and returns the new commit hash.
func commitWT(t *testing.T, wt *git.Worktree, msg string) plumbing.Hash {
	t.Helper()
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig(), AllowEmptyCommits: true})
	if err != nil {
		t.Fatalf("wt.Commit(%s): %v", msg, err)
	}
	return h
}

// hunksEqual compares two hunk slices field-for-field.
func hunksEqual(a, b []Hunk) bool {
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

func TestDiffLines_HunkStructure(t *testing.T) {
	tests := []struct {
		name   string
		parent []string
		child  []string
		want   []Hunk
	}{
		{
			name:   "identical file is one Equal hunk (identity)",
			parent: []string{"a", "b", "c"},
			child:  []string{"a", "b", "c"},
			want: []Hunk{
				{Kind: Equal, ParentStart: 1, ParentLen: 3, ChildStart: 1, ChildLen: 3},
			},
		},
		{
			name:   "insertion at top shifts the equal block's parent mapping",
			parent: []string{"a", "b", "c"},
			child:  []string{"x", "y", "a", "b", "c"},
			want: []Hunk{
				{Kind: Added, ParentStart: 1, ParentLen: 0, ChildStart: 1, ChildLen: 2},
				{Kind: Equal, ParentStart: 1, ParentLen: 3, ChildStart: 3, ChildLen: 3},
			},
		},
		{
			name:   "insertion in the middle",
			parent: []string{"a", "c"},
			child:  []string{"a", "b", "c"},
			want: []Hunk{
				{Kind: Equal, ParentStart: 1, ParentLen: 1, ChildStart: 1, ChildLen: 1},
				{Kind: Added, ParentStart: 2, ParentLen: 0, ChildStart: 2, ChildLen: 1},
				{Kind: Equal, ParentStart: 2, ParentLen: 1, ChildStart: 3, ChildLen: 1},
			},
		},
		{
			name:   "deletion in the middle",
			parent: []string{"a", "b", "c"},
			child:  []string{"a", "c"},
			want: []Hunk{
				{Kind: Equal, ParentStart: 1, ParentLen: 1, ChildStart: 1, ChildLen: 1},
				{Kind: Deleted, ParentStart: 2, ParentLen: 1, ChildStart: 2, ChildLen: 0},
				{Kind: Equal, ParentStart: 3, ParentLen: 1, ChildStart: 2, ChildLen: 1},
			},
		},
		{
			name:   "single-line replacement is Modified",
			parent: []string{"a", "b", "c"},
			child:  []string{"a", "B", "c"},
			want: []Hunk{
				{Kind: Equal, ParentStart: 1, ParentLen: 1, ChildStart: 1, ChildLen: 1},
				{Kind: Modified, ParentStart: 2, ParentLen: 1, ChildStart: 2, ChildLen: 1},
				{Kind: Equal, ParentStart: 3, ParentLen: 1, ChildStart: 3, ChildLen: 1},
			},
		},
		{
			name:   "uneven replacement (2 parent lines -> 3 child lines)",
			parent: []string{"a", "b", "c", "d"},
			child:  []string{"a", "X", "Y", "Z", "d"},
			want: []Hunk{
				{Kind: Equal, ParentStart: 1, ParentLen: 1, ChildStart: 1, ChildLen: 1},
				{Kind: Modified, ParentStart: 2, ParentLen: 2, ChildStart: 2, ChildLen: 3},
				{Kind: Equal, ParentStart: 4, ParentLen: 1, ChildStart: 5, ChildLen: 1},
			},
		},
		{
			name:   "file added (empty parent) is all Added",
			parent: nil,
			child:  []string{"a", "b"},
			want: []Hunk{
				{Kind: Added, ParentStart: 1, ParentLen: 0, ChildStart: 1, ChildLen: 2},
			},
		},
		{
			name:   "file deleted (empty child) is all Deleted",
			parent: []string{"a", "b"},
			child:  nil,
			want: []Hunk{
				{Kind: Deleted, ParentStart: 1, ParentLen: 2, ChildStart: 1, ChildLen: 0},
			},
		},
		{
			name:   "two empty files produce no hunks",
			parent: nil,
			child:  nil,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffLines(tt.parent, tt.child)
			if !hunksEqual(got, tt.want) {
				t.Errorf("diffLines mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

// TestDiffLines_ChildToParentMapping exercises the headline use case: take a
// child line below an edit and resolve it back to the right parent line.
func TestDiffLines_ChildToParentMapping(t *testing.T) {
	// Two lines inserted at the top; "c" sits at child line 5 / parent line 3.
	parent := []string{"a", "b", "c"}
	child := []string{"x", "y", "a", "b", "c"}
	hunks := diffLines(parent, child)

	// mapChildLine mirrors the math a caller would do against the hunk list.
	mapChildLine := func(n int) (parentLine int, introduced bool) {
		for _, h := range hunks {
			if h.ChildLen == 0 {
				continue // Deleted hunks occupy no child lines
			}
			if n >= h.ChildStart && n < h.ChildStart+h.ChildLen {
				if h.Kind == Equal {
					return h.ParentStart + (n - h.ChildStart), false
				}
				return 0, true // Added / Modified: introduced at child
			}
		}
		t.Fatalf("child line %d not covered by any hunk", n)
		return 0, false
	}

	cases := []struct {
		childLine      int
		wantParent     int
		wantIntroduced bool
	}{
		{1, 0, true},  // "x" — newly added
		{2, 0, true},  // "y" — newly added
		{3, 1, false}, // "a" -> parent line 1
		{4, 2, false}, // "b" -> parent line 2
		{5, 3, false}, // "c" -> parent line 3 (shift accounted for)
	}
	for _, c := range cases {
		gotParent, gotIntro := mapChildLine(c.childLine)
		if gotIntro != c.wantIntroduced || (!gotIntro && gotParent != c.wantParent) {
			t.Errorf("child line %d: got (parent=%d, introduced=%v), want (parent=%d, introduced=%v)",
				c.childLine, gotParent, gotIntro, c.wantParent, c.wantIntroduced)
		}
	}
}

func TestDiffFile_FileLevelStatus(t *testing.T) {
	r, fs, wt := newInMemoryRepo(t)

	// c1: file.txt exists. c2: file.txt modified + added.txt introduced.
	c1 := commitFile(t, fs, wt, "file.txt", "a\nb\nc\n", "c1")
	// modify file.txt and add a second file in the same commit.
	writeFile(t, fs, "file.txt", "a\nB\nc\n")
	writeFile(t, fs, "added.txt", "new\n")
	addPath(t, wt, "file.txt")
	addPath(t, wt, "added.txt")
	c2 := commitWT(t, wt, "c2")

	child, err := r.CommitByHash(c2.String())
	if err != nil {
		t.Fatalf("CommitByHash(c2): %v", err)
	}
	parent, err := r.CommitByHash(c1.String())
	if err != nil {
		t.Fatalf("CommitByHash(c1): %v", err)
	}

	t.Run("modified file", func(t *testing.T) {
		fd, err := r.DiffFile(child, parent, "file.txt")
		if err != nil {
			t.Fatalf("DiffFile: %v", err)
		}
		if fd.Status != FileModified {
			t.Errorf("status: got %v, want FileModified", fd.Status)
		}
		// Reuses FileAt's newline model: 3 lines, not 4 from the trailing "\n".
		want := []Hunk{
			{Kind: Equal, ParentStart: 1, ParentLen: 1, ChildStart: 1, ChildLen: 1},
			{Kind: Modified, ParentStart: 2, ParentLen: 1, ChildStart: 2, ChildLen: 1},
			{Kind: Equal, ParentStart: 3, ParentLen: 1, ChildStart: 3, ChildLen: 1},
		}
		if !hunksEqual(fd.Hunks, want) {
			t.Errorf("hunks mismatch\n got: %+v\nwant: %+v", fd.Hunks, want)
		}
	})

	t.Run("added file (absent in parent)", func(t *testing.T) {
		fd, err := r.DiffFile(child, parent, "added.txt")
		if err != nil {
			t.Fatalf("DiffFile: %v", err)
		}
		if fd.Status != FileAdded {
			t.Errorf("status: got %v, want FileAdded", fd.Status)
		}
		want := []Hunk{{Kind: Added, ParentStart: 1, ParentLen: 0, ChildStart: 1, ChildLen: 1}}
		if !hunksEqual(fd.Hunks, want) {
			t.Errorf("hunks mismatch\n got: %+v\nwant: %+v", fd.Hunks, want)
		}
	})

	t.Run("deleted file (absent in child)", func(t *testing.T) {
		// Reverse direction: treat c1 as child of c2 so added.txt is "missing in child".
		fd, err := r.DiffFile(parent, child, "added.txt")
		if err != nil {
			t.Fatalf("DiffFile: %v", err)
		}
		if fd.Status != FileDeleted {
			t.Errorf("status: got %v, want FileDeleted", fd.Status)
		}
		want := []Hunk{{Kind: Deleted, ParentStart: 1, ParentLen: 1, ChildStart: 1, ChildLen: 0}}
		if !hunksEqual(fd.Hunks, want) {
			t.Errorf("hunks mismatch\n got: %+v\nwant: %+v", fd.Hunks, want)
		}
	})

	t.Run("unchanged file is identity", func(t *testing.T) {
		// file.txt at c1 vs c1 — same commit on both sides.
		fd, err := r.DiffFile(parent, parent, "file.txt")
		if err != nil {
			t.Fatalf("DiffFile: %v", err)
		}
		if fd.Status != FileUnchanged {
			t.Errorf("status: got %v, want FileUnchanged", fd.Status)
		}
		want := []Hunk{{Kind: Equal, ParentStart: 1, ParentLen: 3, ChildStart: 1, ChildLen: 3}}
		if !hunksEqual(fd.Hunks, want) {
			t.Errorf("hunks mismatch\n got: %+v\nwant: %+v", fd.Hunks, want)
		}
	})

	t.Run("path absent in both commits returns ErrFileNotFound", func(t *testing.T) {
		_, err := r.DiffFile(child, parent, "ghost.txt")
		if !errors.Is(err, ErrFileNotFound) {
			t.Errorf("got %v, want ErrFileNotFound", err)
		}
	})
}

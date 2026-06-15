package blame

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/realstephendong/sblame/internal/types"
)

// TestClassifyStep_TreeSitterRename exercises the cross-language structural
// rename tier through ClassifyStep, the same harness TestClassifyStep uses for
// Go. A consistent identifier rename is a cosmetic skip; a genuine edit (a
// changed literal, a one-off symbol swap, a structural change) is authored.
func TestClassifyStep_TreeSitterRename(t *testing.T) {
	child := mkCommit(2, "bob")
	parent := mkCommit(1, "alice")

	tests := []struct {
		name       string
		path       string
		parentLine []string
		childLine  []string
		lr         types.LineRange
		wantClass  types.Classification
		wantRange  types.LineRange
	}{
		{
			name: "python consistent rename is cosmetic",
			path: "calc.py",
			parentLine: []string{
				"def run(x):",
				"    return data + data",
			},
			childLine: []string{
				"def run(x):",
				"    return info + info",
			},
			lr:        types.LineRange{FilePath: "calc.py", Start: 2, End: 2},
			wantClass: types.COSMETIC,
			wantRange: types.LineRange{FilePath: "calc.py", Start: 2, End: 2},
		},
		{
			name: "javascript consistent rename is cosmetic",
			path: "f.js",
			parentLine: []string{
				"function f(x) {",
				"  return data + data;",
				"}",
			},
			childLine: []string{
				"function f(x) {",
				"  return info + info;",
				"}",
			},
			lr:        types.LineRange{FilePath: "f.js", Start: 2, End: 2},
			wantClass: types.COSMETIC,
			wantRange: types.LineRange{FilePath: "f.js", Start: 2, End: 2},
		},
		{
			name: "typescript consistent rename is cosmetic",
			path: "f.ts",
			parentLine: []string{
				"function f(x: number): number {",
				"  return count + count;",
				"}",
			},
			childLine: []string{
				"function f(x: number): number {",
				"  return total + total;",
				"}",
			},
			lr:        types.LineRange{FilePath: "f.ts", Start: 2, End: 2},
			wantClass: types.COSMETIC,
			wantRange: types.LineRange{FilePath: "f.ts", Start: 2, End: 2},
		},
		{
			name: "ruby consistent rename is cosmetic",
			path: "f.rb",
			parentLine: []string{
				"def run(x)",
				"  data + data",
				"end",
			},
			childLine: []string{
				"def run(x)",
				"  info + info",
				"end",
			},
			lr:        types.LineRange{FilePath: "f.rb", Start: 2, End: 2},
			wantClass: types.COSMETIC,
			wantRange: types.LineRange{FilePath: "f.rb", Start: 2, End: 2},
		},
		{
			// A changed string literal with unchanged identifiers is not a rename;
			// the literal is a code token, so the change is authored.
			name: "python changed string literal is authored",
			path: "g.py",
			parentLine: []string{
				"def g(x):",
				`    return x + "a"`,
			},
			childLine: []string{
				"def g(x):",
				`    return x + "b"`,
			},
			lr:        types.LineRange{FilePath: "g.py", Start: 2, End: 2},
			wantClass: types.AUTHORED,
		},
		{
			// A single-occurrence identifier change could be a real symbol swap
			// (calling a different function), so it is authored, not a rename.
			name: "python single-occurrence swap is authored",
			path: "h.py",
			parentLine: []string{
				"def h():",
				"    return foo()",
			},
			childLine: []string{
				"def h():",
				"    return bar()",
			},
			lr:        types.LineRange{FilePath: "h.py", Start: 2, End: 2},
			wantClass: types.AUTHORED,
		},
		{
			// A structural change (an added argument) is not a rename even though
			// an identifier also changed consistently.
			name: "python structural change is authored",
			path: "k.py",
			parentLine: []string{
				"def k(amount):",
				"    return amount + amount",
			},
			childLine: []string{
				"def k(value, extra):",
				"    return value + value",
			},
			lr:        types.LineRange{FilePath: "k.py", Start: 2, End: 2},
			wantClass: types.AUTHORED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeHistory{lines: map[plumbing.Hash][]string{
				child.Hash:  tt.childLine,
				parent.Hash: tt.parentLine,
			}}

			step, err := ClassifyStep(f, child, parent, tt.lr)
			if err != nil {
				t.Fatalf("ClassifyStep: %v", err)
			}
			if step.Classification != tt.wantClass {
				t.Fatalf("classification: got %s, want %s", step.Classification, tt.wantClass)
			}
			if tt.wantClass == types.COSMETIC || tt.wantClass == types.UNCHANGED {
				if step.NewRange != tt.wantRange {
					t.Errorf("new range: got %+v, want %+v", step.NewRange, tt.wantRange)
				}
				// A rename edits real code, so a rename skip is never fully certain.
				if step.Confidence <= 0 || step.Confidence >= 1.0 {
					t.Errorf("confidence: got %v, want in (0, 1) for a rename skip", step.Confidence)
				}
			}
		})
	}
}

// TestRun_SkipsCrossLanguageRename walks full history: alice writes Python logic,
// bob renames an identifier consistently, so the line stays alice's at reduced
// confidence.
func TestRun_SkipsCrossLanguageRename(t *testing.T) {
	f, commits := build(t, []struct {
		author string
		lines  []string
	}{
		{"alice", []string{"def f(amount):", "    return amount + amount"}},
		{"bob", []string{"def f(value):", "    return value + value"}},
	})

	result, err := Run(f, commits[1], types.LineRange{FilePath: "f.py", Start: 2, End: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Author != "alice" {
		t.Errorf("author: got %q, want %q (consistent rename should be skipped)", result.Author, "alice")
	}
	if result.Confidence >= 1.0 {
		t.Errorf("confidence: got %v, want < 1.0 for a rename skip", result.Confidence)
	}
}

func TestLexerForPath(t *testing.T) {
	tests := []struct {
		path    string
		wantNil bool
	}{
		{"main.go", false},  // go/scanner lexer
		{"app.py", false},   // tree-sitter
		{"app.JS", false},   // case-insensitive extension
		{"lib.rs", false},   // tree-sitter
		{"notes.txt", true}, // unsupported
		{"Makefile", true},  // no extension
		{"data.json", true}, // no rename lexer registered
	}
	for _, tt := range tests {
		got := lexerForPath(tt.path)
		if (got == nil) != tt.wantNil {
			t.Errorf("lexerForPath(%q): got nil=%v, want nil=%v", tt.path, got == nil, tt.wantNil)
		}
	}
}

func TestIsIdentType(t *testing.T) {
	idents := []string{"identifier", "field_identifier", "type_identifier", "simple_identifier", "constant", "instance_variable"}
	for _, ty := range idents {
		if !isIdentType(ty) {
			t.Errorf("isIdentType(%q) = false, want true", ty)
		}
	}
	nonIdents := []string{"string", "integer", "comment", "string_content", "true"}
	for _, ty := range nonIdents {
		if isIdentType(ty) {
			t.Errorf("isIdentType(%q) = true, want false", ty)
		}
	}
}

package blame

// Rung 2 (structural): consistent-rename detection for Go, via the standard
// library lexer (go/scanner). A change is a rename — cosmetic for the purpose of
// authorship — when one identifier was renamed to another without altering the
// code's structure. Tokens alone cannot judge this safely: on a single line,
// `foo()` -> `bar()` (calling a different function, a real change) looks exactly
// like `userId` -> `userID` (a rename). What distinguishes them is the rest of
// the file: a real rename is consistent everywhere the identifier appears.
//
// So this operates on the WHOLE file, not the hunk: the change qualifies as a
// rename only when the entire parent->child diff is a consistent identifier
// bijection — same token structure, identical literals and operators, and every
// renamed identifier occurring at least twice (so a one-off symbol swap is never
// treated as a rename). When it qualifies, every modified hunk in that file is a
// cosmetic rename. The lexer (not the parser) is used deliberately: it classifies
// identifiers vs keywords vs literals correctly while tolerating the partial
// fragments a hunk's blocks contain, which a full parser would reject.
//
// Why the lexer handles whitespace and comments for free: go/scanner discards
// both, so a change that only reformats or recomments produces no token
// difference and is simply "nothing renamed" here, handled by the token tier.

import (
	"go/scanner"
	gotoken "go/token"
	"strings"
)

// renamePenalty is the most a consistent-rename skip can reduce confidence,
// reached when every identifier on the line was renamed. It is larger than the
// comment penalty because a rename edits real code (identifiers), even though a
// verified global bijection makes it a safe skip.
const renamePenalty = 0.2

// goTok is a lexed token reduced to what rename comparison needs: its kind and,
// for identifiers and literals, its text.
type goTok struct {
	tok gotoken.Token
	lit string
}

// isGoFile reports whether path names a Go source file.
func isGoFile(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".go")
}

// lexGo tokenizes the lines as Go source, discarding comments. ok is false if
// the input contains a lexical error (e.g. an unterminated string), in which
// case the caller falls back to the token tier.
func lexGo(lines []string) (toks []goTok, ok bool) {
	src := []byte(strings.Join(lines, "\n"))
	var s scanner.Scanner
	fset := gotoken.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, src, nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == gotoken.EOF {
			break
		}
		toks = append(toks, goTok{tok: tok, lit: lit})
	}
	if s.ErrorCount > 0 {
		return nil, false
	}
	return toks, true
}

// detectGoRename reports whether the entire difference between two versions of a
// Go file is a consistent identifier rename. See the file comment for the rules.
func detectGoRename(parentLines, childLines []string) bool {
	p, okp := lexGo(parentLines)
	c, okc := lexGo(childLines)
	if !okp || !okc || len(p) != len(c) || len(p) == 0 {
		return false
	}

	forward := map[string]string{} // old name -> new name
	reverse := map[string]string{} // new name -> old name
	count := map[string]int{}      // occurrences of each old name
	renamed := map[string]bool{}   // old names that actually changed

	for i := range p {
		pt, ct := p[i], c[i]
		if pt.tok != ct.tok {
			return false // structural difference
		}
		switch {
		case pt.tok == gotoken.IDENT:
			count[pt.lit]++
			if v, ok := forward[pt.lit]; ok {
				if v != ct.lit {
					return false // same old name maps to two new names
				}
			} else {
				forward[pt.lit] = ct.lit
			}
			if v, ok := reverse[ct.lit]; ok {
				if v != pt.lit {
					return false // two old names collapse to one new name
				}
			} else {
				reverse[ct.lit] = pt.lit
			}
			if pt.lit != ct.lit {
				renamed[pt.lit] = true
			}
		case pt.tok.IsLiteral():
			if pt.lit != ct.lit {
				return false // different number/string/char literal
			}
			// operators and keywords are fully determined by tok, already equal.
		}
	}

	if len(renamed) == 0 {
		return false // identical structure, nothing renamed
	}
	for old := range renamed {
		if count[old] < 2 {
			return false // single-occurrence change is too ambiguous to call a rename
		}
	}
	return true
}

// goRenameRatio is the fraction of identifier positions in a hunk's blocks whose
// name changed, used to scale the rename confidence penalty so a line where one
// name changed is hedged less than one where every name did. It assumes the file
// already qualified as a rename via detectGoRename; on any lexing surprise it
// returns 1.0 (maximum penalty), the conservative choice.
func goRenameRatio(parentBlock, childBlock []string) float64 {
	p, okp := lexGo(parentBlock)
	c, okc := lexGo(childBlock)
	if !okp || !okc || len(p) != len(c) {
		return 1.0
	}
	total, changed := 0, 0
	for i := range p {
		if p[i].tok == gotoken.IDENT && c[i].tok == gotoken.IDENT {
			total++
			if p[i].lit != c[i].lit {
				changed++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(changed) / float64(total)
}

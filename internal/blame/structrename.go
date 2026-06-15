package blame

// Rung 2 (structural): consistent-rename detection, generalized across languages.
// A change is a rename — cosmetic for the purpose of authorship — when one
// identifier was renamed to another without altering the code's structure.
// Tokens alone cannot judge this safely: on a single line, `foo()` -> `bar()`
// (calling a different function, a real change) looks exactly like `userId` ->
// `userID` (a rename). What distinguishes them is the rest of the file: a real
// rename is consistent everywhere the identifier appears.
//
// So this operates on the WHOLE file, not the hunk: the change qualifies as a
// rename only when the entire parent->child diff is a consistent identifier
// bijection — same token structure, identical literals and operators, and every
// renamed identifier occurring at least twice (so a one-off symbol swap is never
// treated as a rename). When it qualifies, every modified hunk in that file is a
// cosmetic rename.
//
// The analysis is the same for every language; only tokenization differs. A
// structLexer turns source lines into a flat stream of structural tokens —
// identifiers (renameable) and everything else (keywords, operators,
// punctuation, literals: compared by exact text) — discarding whitespace and
// comments. Go uses the standard library lexer (go/scanner; see gorename.go);
// other languages use tree-sitter (see treesitter.go). Because whitespace and
// comments are discarded, a change that only reformats or recomments produces no
// token difference and is simply "nothing renamed" here, left to the token and
// comment tiers.

// renamePenalty is the most a consistent-rename skip can reduce confidence,
// reached when every identifier on the line was renamed. It is larger than the
// comment penalty because a rename edits real code (identifiers), even though a
// verified global bijection makes it a safe skip.
const renamePenalty = 0.2

// structKind separates the two ways a structural token participates in rename
// analysis.
type structKind uint8

const (
	// structOther is any token compared by exact text and never renamed:
	// keywords, operators, punctuation, and literals. A difference in any of
	// these is a structural change, disqualifying the diff as a pure rename.
	structOther structKind = iota
	// structIdent is a renameable identifier, the only token a consistent
	// rename is allowed to change.
	structIdent
)

// structTok is a lexed token reduced to what rename comparison needs: whether it
// is a renameable identifier, and its comparison text. For an identifier the
// text is its name; for everything else it is the literal value or the canonical
// keyword/operator spelling, so two tokens compare equal exactly when they are
// the same structural element.
type structTok struct {
	kind structKind
	text string
}

// structLexer tokenizes source lines into structural tokens for a particular
// language, discarding whitespace and comments. ok is false on a lexical or
// parse error, in which case the caller falls back to the token tier.
type structLexer interface {
	lex(lines []string) (toks []structTok, ok bool)
}

// lexerForPath returns the structural lexer for a file path, or nil when the
// language is unsupported (rename detection is then skipped, which is safe).
func lexerForPath(filePath string) structLexer {
	if isGoFile(filePath) {
		return goLexer{}
	}
	return tsLexerForPath(filePath)
}

// detectRename reports whether the entire difference between two versions of a
// file is a consistent identifier rename. See the file comment for the rules.
func detectRename(lx structLexer, parentLines, childLines []string) bool {
	p, okp := lx.lex(parentLines)
	c, okc := lx.lex(childLines)
	if !okp || !okc || len(p) != len(c) || len(p) == 0 {
		return false
	}

	forward := map[string]string{} // old name -> new name
	reverse := map[string]string{} // new name -> old name
	count := map[string]int{}      // occurrences of each old name
	renamed := map[string]bool{}   // old names that actually changed

	for i := range p {
		pt, ct := p[i], c[i]
		if pt.kind != ct.kind {
			return false // structural difference (e.g. identifier became a literal)
		}
		if pt.kind == structOther {
			if pt.text != ct.text {
				return false // different literal, keyword, or operator
			}
			continue
		}
		// Identifier: record the bijection.
		count[pt.text]++
		if v, ok := forward[pt.text]; ok {
			if v != ct.text {
				return false // same old name maps to two new names
			}
		} else {
			forward[pt.text] = ct.text
		}
		if v, ok := reverse[ct.text]; ok {
			if v != pt.text {
				return false // two old names collapse to one new name
			}
		} else {
			reverse[ct.text] = pt.text
		}
		if pt.text != ct.text {
			renamed[pt.text] = true
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

// renameRatio is the fraction of identifier positions in a hunk's blocks whose
// name changed, used to scale the rename confidence penalty so a line where one
// name changed is hedged less than one where every name did. It assumes the file
// already qualified as a rename via detectRename; on any tokenization surprise it
// returns 1.0 (maximum penalty), the conservative choice.
func renameRatio(lx structLexer, parentBlock, childBlock []string) float64 {
	p, okp := lx.lex(parentBlock)
	c, okc := lx.lex(childBlock)
	if !okp || !okc || len(p) != len(c) {
		return 1.0
	}
	total, changed := 0, 0
	for i := range p {
		if p[i].kind == structIdent && c[i].kind == structIdent {
			total++
			if p[i].text != c[i].text {
				changed++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(changed) / float64(total)
}

package blame

// The Go structLexer: tokenization via the standard library lexer (go/scanner),
// feeding the language-general rename analysis in structrename.go. The lexer (not
// the parser) is used deliberately: it classifies identifiers vs keywords vs
// literals correctly while tolerating the partial fragments a hunk's blocks
// contain, which a full parser would reject. go/scanner also discards whitespace
// and comments for free, so reformatting or recommenting produces no token
// difference.
//
// Go keeps its own standard-library lexer rather than routing through
// tree-sitter: it is pure Go (no cgo), already battle-tested, and the most
// common input this tool sees.

import (
	"go/scanner"
	gotoken "go/token"
	"strings"
)

// isGoFile reports whether path names a Go source file.
func isGoFile(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".go")
}

// goLexer tokenizes Go source with go/scanner.
type goLexer struct{}

// lex tokenizes the lines as Go source, discarding comments and whitespace. ok
// is false if the input contains a lexical error (e.g. an unterminated string),
// in which case the caller falls back to the token tier.
func (goLexer) lex(lines []string) (toks []structTok, ok bool) {
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
		toks = append(toks, goStructTok(tok, lit))
	}
	if s.ErrorCount > 0 {
		return nil, false
	}
	return toks, true
}

// goStructTok maps a go/scanner token to a structTok. IDENT is the only
// renameable kind; literals compare by their text; keywords, operators, and
// punctuation compare by their canonical spelling (tok.String()), which is
// unique per token kind. IDENT is checked before IsLiteral because go/token
// classifies IDENT as a literal.
func goStructTok(tok gotoken.Token, lit string) structTok {
	switch {
	case tok == gotoken.IDENT:
		return structTok{kind: structIdent, text: lit}
	case tok.IsLiteral():
		return structTok{kind: structOther, text: lit}
	default:
		return structTok{kind: structOther, text: tok.String()}
	}
}

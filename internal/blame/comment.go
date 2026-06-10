package blame

// Per-language comment and string syntax, consumed by the tokenizer (token.go)
// to tell code from comments when deciding whether a change is cosmetic.
//
// Correctness bias: a comment delimiter inside a string literal (e.g.
// "http://example.com") is NOT a comment, so the tokenizer is string-literal
// aware and always errs toward treating ambiguous input as code. Mistaking a
// comment for code only costs a missed skip (sblame degrades to plain git blame
// for that step); mistaking code for a comment would cause a false skip and
// wrong attribution, which is what the bias avoids.
//
// Known limits (all err toward code, so toward safety): string state is not
// carried across line boundaries, so a `//` on a continuation line of a Go
// backtick raw string is not recognized as string content; Python triple-quoted
// docstrings are treated as code, not comments.

import (
	"path"
	"strings"
)

// commentStyle describes the comment and string-literal syntax of a language.
type commentStyle struct {
	// lineComments are prefixes that comment out the rest of the line.
	lineComments []string
	// blockOpen/blockClose delimit a comment that may span lines. Empty
	// blockOpen means the language has no block comments.
	blockOpen  string
	blockClose string
	// quotes are the string-literal delimiters. A delimiter inside a string is
	// not treated as a comment start.
	quotes []byte
}

var (
	cStyle    = commentStyle{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: []byte{'"', '\'', '`'}}
	hashStyle = commentStyle{lineComments: []string{"#"}, quotes: []byte{'"', '\''}}
	dashStyle = commentStyle{lineComments: []string{"--"}, quotes: []byte{'"', '\''}}
)

// extStyles maps a lowercased file extension to its comment style. Extensions
// absent here have no known style; the caller falls back to whitespace-only
// cosmetic detection, which is always safe.
var extStyles = map[string]commentStyle{
	// C-family: // line, /* */ block.
	".go": cStyle, ".c": cStyle, ".h": cStyle, ".cpp": cStyle, ".cc": cStyle,
	".cxx": cStyle, ".hpp": cStyle, ".hh": cStyle, ".java": cStyle, ".js": cStyle,
	".jsx": cStyle, ".ts": cStyle, ".tsx": cStyle, ".rs": cStyle, ".swift": cStyle,
	".kt": cStyle, ".kts": cStyle, ".scala": cStyle, ".cs": cStyle, ".php": cStyle,
	".dart": cStyle, ".m": cStyle, ".mm": cStyle,
	// Hash-comment family.
	".py": hashStyle, ".rb": hashStyle, ".sh": hashStyle, ".bash": hashStyle,
	".zsh": hashStyle, ".yaml": hashStyle, ".yml": hashStyle, ".toml": hashStyle,
	".pl": hashStyle, ".r": hashStyle,
	// Dash-comment family.
	".sql": dashStyle, ".lua": dashStyle, ".hs": dashStyle,
}

// styleForPath returns the comment style for a file path and whether one is
// known. An unknown extension returns ok == false, signaling the caller to use
// whitespace-only cosmetic detection.
func styleForPath(filePath string) (commentStyle, bool) {
	style, ok := extStyles[strings.ToLower(path.Ext(filePath))]
	return style, ok
}

// startsLineComment reports whether s begins with any of the line-comment
// prefixes.
func startsLineComment(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// isQuote reports whether c is one of the string-literal delimiters.
func isQuote(c byte, quotes []byte) bool {
	for _, q := range quotes {
		if c == q {
			return true
		}
	}
	return false
}

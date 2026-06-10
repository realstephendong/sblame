package blame

// Comment-aware "code skeleton" extraction, used to decide whether a
// modification is cosmetic.
//
// The engine treats a change as COSMETIC when the lines differ only in ways
// that cannot affect behavior. The simplest such difference is whitespace; the
// next, handled here, is comments. Two blocks that are identical once their
// comments AND whitespace are removed differ only cosmetically, so the engine
// skips the commit and keeps looking for genuine authorship.
//
// Correctness bias: stripping comments is the hard part, because a comment
// delimiter inside a string literal (e.g. "http://example.com") is NOT a
// comment. A naive strip would corrupt such a line and report two genuinely
// different lines as equal — a FALSE cosmetic classification, i.e. wrong blame.
// The scanner here is therefore string-literal aware and always errs toward
// treating ambiguous input as code:
//
//   - Mistaking a comment for code  -> a missed skip -> sblame degrades to plain
//     `git blame` for that step. Safe.
//   - Mistaking code for a comment  -> a false skip -> wrong attribution. Not
//     safe, and what this scanner is built to avoid.
//
// Known limits (all err toward code, so toward safety): string state is not
// carried across line boundaries, so a `//` on a continuation line of a Go
// backtick raw string is not recognized as string content; Python triple-quoted
// docstrings are treated as code, not comments.

import (
	"path"
	"strings"
	"unicode"
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

// codeSkeleton returns the lines with comments and all whitespace removed, so
// two blocks compare equal iff they differ only in comments and/or whitespace.
// inBlock state for block comments is carried across the block's lines.
func codeSkeleton(lines []string, style commentStyle) string {
	var b strings.Builder
	inBlock := false
	for _, line := range lines {
		var code string
		code, inBlock = stripLineComments(line, style, inBlock)
		for _, r := range code {
			if !unicode.IsSpace(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// stripLineComments removes comments from a single line, given whether the line
// begins inside a block comment, and reports whether it ends inside one. String
// literals are preserved verbatim (they are code), and comment delimiters found
// inside them are ignored.
func stripLineComments(line string, style commentStyle, inBlock bool) (code string, stillInBlock bool) {
	var b strings.Builder
	i := 0
	for i < len(line) {
		if inBlock {
			if style.blockClose != "" && strings.HasPrefix(line[i:], style.blockClose) {
				inBlock = false
				i += len(style.blockClose)
				continue
			}
			i++ // drop the commented-out character
			continue
		}

		c := line[i]

		// String literal: copy it through, honoring backslash escapes, so any
		// comment delimiter inside it is never mistaken for a comment.
		if isQuote(c, style.quotes) {
			b.WriteByte(c)
			i++
			for i < len(line) {
				ch := line[i]
				if ch == '\\' && i+1 < len(line) {
					b.WriteByte(ch)
					b.WriteByte(line[i+1])
					i += 2
					continue
				}
				b.WriteByte(ch)
				i++
				if ch == c {
					break // closing quote
				}
			}
			continue
		}

		// Block comment start.
		if style.blockOpen != "" && strings.HasPrefix(line[i:], style.blockOpen) {
			inBlock = true
			i += len(style.blockOpen)
			continue
		}

		// Line comment: the rest of the line is a comment.
		if startsLineComment(line[i:], style.lineComments) {
			break
		}

		b.WriteByte(c)
		i++
	}
	return b.String(), inBlock
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

package blame

// Token-level (lexical) analysis of a modification, the structural basis for
// deciding whether a change is cosmetic.
//
// The earlier approach compared lines as normalized strings (strip whitespace,
// strip comments, compare). That conflates things a lexer keeps apart: it would
// call "ab" and "a b" equal (whitespace stripped), and it stripped comment
// delimiters with a scanner that could be fooled by strings. Tokenizing fixes
// both: a change is cosmetic exactly when the sequence of CODE tokens is
// unchanged, so only whitespace and/or comments moved. "a+b" and "a + b"
// tokenize identically (cosmetic); joining "a b" into "ab" does not (authored);
// a change inside a string literal is a code-token change (authored), because
// the lexer treats a whole string as one code token.
//
// Confidence is then derived rather than guessed: a cosmetic change whose
// comment tokens are also identical is pure whitespace and fully certain; one
// whose comments changed is scored by how much of the comment text changed, so
// a one-word tweak barely dents confidence while a full rewrite dents it more.

import (
	"strings"

	"github.com/realstephendong/sblame/internal/gitlayer"
)

// tokenClass distinguishes code from comments. Whitespace is a separator and
// never becomes a token.
type tokenClass int

const (
	codeToken tokenClass = iota
	commentToken
)

// commentPenalty is the most a single comment-only skip can reduce confidence,
// reached when the comment changed completely. A partial comment change reduces
// confidence proportionally less. The code itself is unchanged in either case
// (that is what makes it a skip), so the penalty is small: the only residual
// risk is that the lexer misjudged what was a comment.
const commentPenalty = 0.1

// token is one lexical unit: an identifier, number, operator/punctuation run,
// string literal (a single code token), or a whitespace-delimited piece of a
// comment.
type token struct {
	text  string
	class tokenClass
}

// analyzeModification reports whether a Modified hunk's change is cosmetic —
// i.e. leaves the code tokens unchanged — and, if so, a confidence factor in
// (0, 1] for skipping it.
func analyzeModification(parentLines, childLines []string, hk gitlayer.Hunk, style commentStyle) (cosmetic bool, confidence float64) {
	parentToks := tokenize(sliceLines(parentLines, hk.ParentStart, hk.ParentLen), style)
	childToks := tokenize(sliceLines(childLines, hk.ChildStart, hk.ChildLen), style)

	if !tokensEqual(tokenTexts(parentToks, codeToken), tokenTexts(childToks, codeToken)) {
		return false, 0 // code changed: authored
	}

	parentComments := tokenTexts(parentToks, commentToken)
	childComments := tokenTexts(childToks, commentToken)
	if tokensEqual(parentComments, childComments) {
		return true, confExact // only whitespace moved
	}
	// Comments changed; scale the penalty by how much of the comment text did.
	ratio := tokenDistanceRatio(parentComments, childComments)
	return true, confExact - commentPenalty*ratio
}

// tokenize splits a block of lines into tokens using the language's comment and
// string syntax. Block-comment state is carried across lines. An unknown
// language (zero commentStyle) yields only code tokens split on whitespace,
// which is the safe fallback: comment/string awareness is off, so nothing is
// skipped that a plain whitespace comparison would not also skip.
func tokenize(lines []string, style commentStyle) []token {
	var toks []token
	inBlock := false
	for _, line := range lines {
		inBlock = tokenizeLine(line, style, inBlock, &toks)
	}
	return toks
}

// tokenizeLine appends the tokens of one line and returns whether the line ends
// inside a block comment.
func tokenizeLine(line string, style commentStyle, inBlock bool, out *[]token) bool {
	n := len(line)
	i := 0
	for i < n {
		if inBlock {
			start := i
			for i < n && !(style.blockClose != "" && strings.HasPrefix(line[i:], style.blockClose)) {
				i++
			}
			appendCommentTokens(out, line[start:i])
			if i < n { // found the closing delimiter
				i += len(style.blockClose)
				inBlock = false
			}
			continue
		}

		c := line[i]
		switch {
		case isSpaceByte(c):
			i++
		case startsLineComment(line[i:], style.lineComments):
			appendCommentTokens(out, line[i:])
			i = n
		case style.blockOpen != "" && strings.HasPrefix(line[i:], style.blockOpen):
			i += len(style.blockOpen)
			inBlock = true
		case isQuote(c, style.quotes):
			start := i
			i++
			for i < n {
				if line[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				ch := line[i]
				i++
				if ch == c {
					break // closing quote
				}
			}
			*out = append(*out, token{text: line[start:i], class: codeToken})
		case isWordByte(c):
			start := i
			for i < n && isWordByte(line[i]) {
				i++
			}
			*out = append(*out, token{text: line[start:i], class: codeToken})
		default:
			// A run of operator/punctuation characters, stopping before any
			// whitespace, word, string, or comment that begins mid-run.
			start := i
			for i < n {
				ch := line[i]
				if isSpaceByte(ch) || isWordByte(ch) || isQuote(ch, style.quotes) {
					break
				}
				if startsLineComment(line[i:], style.lineComments) {
					break
				}
				if style.blockOpen != "" && strings.HasPrefix(line[i:], style.blockOpen) {
					break
				}
				i++
			}
			*out = append(*out, token{text: line[start:i], class: codeToken})
		}
	}
	return inBlock
}

// appendCommentTokens splits comment text on whitespace and appends each piece
// as a comment token, so comment changes can be scored at word granularity.
func appendCommentTokens(out *[]token, text string) {
	for _, field := range strings.Fields(text) {
		*out = append(*out, token{text: field, class: commentToken})
	}
}

// tokenTexts returns the texts of the tokens of the given class, in order.
func tokenTexts(toks []token, class tokenClass) []string {
	var out []string
	for _, t := range toks {
		if t.class == class {
			out = append(out, t.text)
		}
	}
	return out
}

// tokensEqual reports whether two token-text sequences are identical.
func tokensEqual(a, b []string) bool {
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

// tokenDistanceRatio measures how different two token sequences are, as the
// number of insertions and deletions (via longest common subsequence) needed to
// turn one into the other, normalized by their combined length. It is 0 for
// identical sequences and approaches 1 as they share less.
func tokenDistanceRatio(a, b []string) float64 {
	total := len(a) + len(b)
	if total == 0 {
		return 0
	}
	edits := total - 2*lcsLen(a, b)
	return float64(edits) / float64(total)
}

// lcsLen returns the length of the longest common subsequence of a and b,
// computed with a rolling row to keep memory at O(min len).
func lcsLen(a, b []string) int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return 0
	}
	prev := make([]int, m+1)
	for i := 1; i <= n; i++ {
		cur := make([]int, m+1)
		for j := 1; j <= m; j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev = cur
	}
	return prev[m]
}

// isWordByte reports whether c can be part of an identifier or number run.
// Bytes >= 0x80 are included so multibyte (UTF-8) identifiers stay intact.
func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c >= 0x80
}

// isSpaceByte reports whether c is intra-line whitespace. Lines are already
// split on '\n', so newlines never reach here.
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f'
}

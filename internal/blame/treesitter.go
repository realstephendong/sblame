package blame

// The tree-sitter structLexer: tokenization for every supported non-Go language,
// feeding the language-general rename analysis in structrename.go.
//
// A tree-sitter parse is a tree whose leaves are the source's tokens. Walking the
// leaves in order yields the flat token stream rename analysis needs:
//
//   - An anonymous leaf is a keyword, operator, or punctuation. Its node type IS
//     its text (e.g. "if", "+", "("), so it becomes a structOther compared by type.
//   - A named leaf is an identifier or a literal. Identifiers (recognized by node
//     type; see isIdentType) are renameable structIdent tokens; everything else
//     (strings, numbers, ...) is a structOther compared by exact text, so a change
//     to a literal disqualifies the diff as a pure rename.
//   - Comment leaves are discarded, matching go/scanner, so a recommenting change
//     produces no token difference here.
//
// Correctness bias mirrors the rest of the tier: misjudging an identifier as a
// literal only costs a missed rename skip (we degrade to plain git blame for that
// step), whereas misjudging a literal as an identifier could license a false
// skip. So isIdentType recognizes only genuinely identifier-like node types; a
// grammar whose identifiers fall outside it simply under-detects renames, never
// over-detects them.

import (
	"context"
	"path"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// tsLangs maps a lowercased file extension to the tree-sitter grammar for its
// language. Extensions absent here have no tree-sitter lexer; rename detection is
// then skipped for that file, which is safe.
var tsLangs = map[string]func() *sitter.Language{
	".py":    python.GetLanguage,
	".js":    javascript.GetLanguage,
	".jsx":   javascript.GetLanguage,
	".mjs":   javascript.GetLanguage,
	".cjs":   javascript.GetLanguage,
	".ts":    typescript.GetLanguage,
	".tsx":   tsx.GetLanguage,
	".java":  java.GetLanguage,
	".c":     c.GetLanguage,
	".h":     c.GetLanguage,
	".cpp":   cpp.GetLanguage,
	".cc":    cpp.GetLanguage,
	".cxx":   cpp.GetLanguage,
	".hpp":   cpp.GetLanguage,
	".hh":    cpp.GetLanguage,
	".rb":    ruby.GetLanguage,
	".rs":    rust.GetLanguage,
	".kt":    kotlin.GetLanguage,
	".kts":   kotlin.GetLanguage,
	".scala": scala.GetLanguage,
	".cs":    csharp.GetLanguage,
	".php":   php.GetLanguage,
	".swift": swift.GetLanguage,
	".lua":   lua.GetLanguage,
	".sh":    bash.GetLanguage,
	".bash":  bash.GetLanguage,
}

// identTypes are tree-sitter node types treated as renameable identifiers. The
// common case ("identifier") and grammar-specific suffixed variants
// ("field_identifier", "type_identifier", "simple_identifier", ...) are matched
// by the "identifier" suffix in isIdentType; this set adds the identifier-like
// types that do not carry that suffix (chiefly Ruby's constant and variable
// forms). None of these is ever a literal, preserving the conservative bias.
var identTypes = map[string]bool{
	"constant":          true, // Ruby: Foo = ...
	"instance_variable": true, // Ruby: @x
	"class_variable":    true, // Ruby: @@x
	"global_variable":   true, // Ruby: $x
}

// isIdentType reports whether a tree-sitter node type denotes a renameable
// identifier.
func isIdentType(nodeType string) bool {
	return identTypes[nodeType] || strings.HasSuffix(nodeType, "identifier")
}

// isCommentType reports whether a node type denotes a comment, which is
// discarded from the token stream.
func isCommentType(nodeType string) bool {
	return strings.Contains(nodeType, "comment")
}

// tsLexerForPath returns a tree-sitter lexer for a file path, or nil when the
// extension has no registered grammar.
func tsLexerForPath(filePath string) structLexer {
	getLang, ok := tsLangs[strings.ToLower(path.Ext(filePath))]
	if !ok {
		return nil
	}
	return tsLexer{lang: getLang()}
}

// tsLexer tokenizes source with a tree-sitter grammar.
type tsLexer struct {
	lang *sitter.Language
}

// lex parses the lines and flattens the tree's leaves into structural tokens. ok
// is false only on a hard parse failure; tree-sitter recovers from syntax errors
// by inserting ERROR nodes, whose real token leaves still surface, so a partial
// hunk fragment still tokenizes. Safety does not rest on a clean parse: the
// bijection and structure checks in detectRename reject any non-rename change
// regardless, and a genuine structural difference shifts the leaf stream so the
// streams no longer align.
func (l tsLexer) lex(lines []string) (toks []structTok, ok bool) {
	src := []byte(strings.Join(lines, "\n"))
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(l.lang)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		return nil, false
	}
	collectLeaves(root, src, &toks)
	return toks, true
}

// collectLeaves appends the structural tokens of n's leaves, in source order.
func collectLeaves(n *sitter.Node, src []byte, out *[]structTok) {
	if n.ChildCount() == 0 {
		// MISSING nodes are zero-width tokens tree-sitter inserts during error
		// recovery; they carry no real source and would desync the stream.
		if n.IsMissing() {
			return
		}
		nodeType := n.Type()
		if isCommentType(nodeType) {
			return // discard comments, matching the Go lexer
		}
		if n.IsNamed() {
			if isIdentType(nodeType) {
				*out = append(*out, structTok{kind: structIdent, text: n.Content(src)})
			} else {
				// A non-identifier named leaf is a literal (string, number, ...):
				// compared by its exact text.
				*out = append(*out, structTok{kind: structOther, text: n.Content(src)})
			}
			return
		}
		// Anonymous leaf: keyword/operator/punctuation. Its type equals its text,
		// so comparing by type makes the match structural.
		*out = append(*out, structTok{kind: structOther, text: nodeType})
		return
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		collectLeaves(n.Child(i), src, out)
	}
}

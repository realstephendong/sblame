# sblame — semantic blame

`sblame` walks git history to find where code logic was *genuinely authored*,
seeing past the cosmetic commits — reformatting, comment edits, identifier
renames, file renames — that plain `git blame` pins on whoever last touched the
line.

Where it can't be certain, it says so: every attribution carries a confidence
score that drops as the walk passes through heuristic skips.

## Build & test

```
make build   # -> ./bin/sblame
make test
```

## Usage

```
sblame <file> [--line N] [--rev REV]   # blame a line (default: line 1, from HEAD)
sblame --eval                          # run the built-in accuracy suite
sblame --languages                     # list language-support tiers
```

Each attribution reports the genuine author, a confidence percentage, and the
commit that introduced the logic:

```
$ sblame internal/blame/blame.go --line 120
alice (100% confidence)
  commit 332a6a01dda81f2da254dd3cf2a57e2fc73cbe38
  date   2026-03-14
  line   internal/blame/blame.go:120
```

## What it does

### Sees past cosmetic commits

The heart of sblame. A change is skipped — authorship stays with the code's real
author — when it doesn't alter the code's meaning. Three graded rungs:

- **Whitespace / formatting** (all files, full confidence). A change is cosmetic
  exactly when the sequence of *code tokens* is unchanged, so `a+b` ≡ `a + b`,
  reindentation, and operator spacing are skipped. It still catches the tricky
  cases: fusing `a b` → `ab` is a real change (new token), and whitespace
  *inside a string literal* is a real change — both are attributed, not skipped.
- **Comment-only edits** (languages with known comment syntax, graded
  confidence). A reworded comment is skipped, scored by *how much* of the comment
  changed: a one-word tweak barely dents confidence, a full rewrite dents it
  more. String-literal aware, so `//` inside `"http://…"` is not a comment.
- **Consistent identifier renames** (languages with a structural lexer, graded
  confidence). Renaming `total` → `amount` everywhere it appears is cosmetic — but
  only when the *whole file* change is a verified identifier bijection (same
  structure, identical literals/operators, each renamed identifier occurring at
  least twice). A one-off `foo()` → `bar()` or an added argument is a real change,
  not a rename.

The bias is always toward *authored*: when a change is ambiguous, sblame
attributes it rather than risk a wrong skip.

### Follows file renames

When a file is renamed (`git mv`), the line's history continues under the old
path instead of stopping at the rename commit — so the rename steals no
authorship. An exact rename (identical content) is followed at full confidence;
a rename that also edits the file is matched by content similarity at reduced
confidence. Rename chains (`a.go` → `b.go` → `c.go`) and rename-plus-edit commits
are handled, and a genuinely new file is told apart from a rename. On files that
crossed a rename, sblame agrees with `git blame -C -C -C`.

### Follows moved code across files

When a block of lines is added to a file but was really moved or copied from
another file the **same commit** changed (a refactor that relocates a function,
say), sblame continues the walk in that source file instead of crediting the
commit that moved it — git's `-C`. To avoid false matches, the moved block must
be substantial (≥ 40 non-whitespace characters, git's default), so trivial lines
like `}` never trigger it. (Copies from files the commit didn't touch — git's
`-C -C` / `-C -C -C` — aren't detected yet.)

### Reports honest confidence

Confidence is derived, not guessed. Each step contributes a factor — `1.0` for an
exact, textual decision; less for a heuristic skip (a comment edit, an identifier
rename, a similarity-matched file rename, a cross-file move) — and they multiply,
so an answer reached past fuzzier steps reports lower overall certainty.

### Language coverage

| Tier | Detection | Languages |
| --- | --- | --- |
| Full structural | rename + comment + whitespace | Go, Python, JavaScript / TypeScript (+ JSX/TSX), Java, C, C++, Ruby, Rust, Kotlin, Scala, C#, PHP, Swift, Lua, Bash |
| Comment-aware | comment + whitespace | Dart, Haskell, Objective-C, Perl, R, SQL, TOML, YAML, Zsh |
| Baseline | whitespace only | every other extension (always safe) |

Run `sblame --languages` for the exact extension list. Go uses the standard
library's `go/scanner`; the other structural languages use tree-sitter.

## How it works

Starting from a line in a commit, the engine walks first-parent history and, at
each step, classifies how the tracked range changed versus the parent —
UNCHANGED and COSMETIC continue the walk (remapping the line), MOVED continues it
in another file (a rename or a cross-file move), and AUTHORED stops and
attributes. It bottoms out at the commit that introduced the logic, or at the
root.

```
cmd/sblame/         CLI entry point
internal/types/     Core domain types (LineRange, Classification, BlameResult)
internal/blame/     Recursive line-range tracking engine + cosmetic classification
internal/gitlayer/  go-git wrappers: history walk, line diff, blob access, rename + copy detection
internal/eval/      Accuracy / regression harness
```

This is a from-scratch project: the diff, the tokenizer, and the rename
similarity matching are all implemented here. go-git is used only as the raw
object store — resolving commits, listing a tree's files, reading a blob's bytes.

## Not yet

- **Broader copy detection** — copies from files the commit didn't change (`-C -C`)
  or from any commit in history (`-C -C -C`), code extracted into a brand-new
  file, and whitespace-tolerant block matching.
- **Merge-parent attribution** — merges follow the first parent only today,
  matching default `git blame`.
- **A systematic agreement harness** against `git blame -C` on a large, messy
  repo — current evaluation is a curated in-memory suite plus spot checks.

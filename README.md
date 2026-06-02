# sblame — Semantic Blame

`sblame` walks git history to find where code logic was genuinely authored, skipping cosmetic commits (whitespace changes, reformatting, renames, moves).

**Status: Month 1 scaffolding** — the project structure, types, and stubs are in place. Core traversal, diffing, and classification logic is not yet implemented.

## Build

```
make build
```

## Test

```
make test
```

## Usage

```
sblame <file> [--line N]
```

## Project structure

```
cmd/sblame/         CLI entry point
internal/types/     Core domain types (Commit, LineRange, Classification, BlameResult)
internal/blame/     Recursive line-range tracking engine (stub)
internal/gitlayer/  go-git wrappers for history walk, diffs, blob access (stub)
internal/eval/      Evaluation harness (stub)
```

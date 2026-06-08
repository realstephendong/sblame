package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/realstephendong/sblame/internal/blame"
	"github.com/realstephendong/sblame/internal/gitlayer"
	"github.com/realstephendong/sblame/internal/types"
)

func main() {
	line := flag.Int("line", 0, "line number to blame (defaults to 1)")
	rev := flag.String("rev", "HEAD", "revision to start the blame walk from")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: sblame <file> [--line N] [--rev REV]\n\n")
		fmt.Fprintf(os.Stderr, "Semantic blame: find where code logic was genuinely authored,\n")
		fmt.Fprintf(os.Stderr, "skipping cosmetic commits.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// Go's flag package stops at the first positional argument, so flags placed
	// after the filename (e.g. "sblame file --line 2") would be ignored. Pull out
	// the filename and re-parse the remainder so flags work in any position.
	rest := flag.Args()
	if len(rest) < 1 {
		flag.Usage()
		os.Exit(1)
	}
	file := rest[0]
	if err := flag.CommandLine.Parse(rest[1:]); err != nil {
		os.Exit(2) // flag already printed the error
	}

	if err := run(file, *line, *rev); err != nil {
		fmt.Fprintf(os.Stderr, "sblame: %v\n", err)
		os.Exit(1)
	}
}

func run(fileArg string, line int, rev string) error {
	absPath, err := filepath.Abs(fileArg)
	if err != nil {
		return err
	}

	repo, err := gitlayer.Open(filepath.Dir(absPath))
	if err != nil {
		return fmt.Errorf("opening repository: %w", err)
	}

	relPath, err := repoRelPath(repo, absPath)
	if err != nil {
		return err
	}

	start, err := repo.ResolveCommit(rev)
	if err != nil {
		return err
	}

	if line < 1 {
		line = 1
	}
	lr := types.LineRange{FilePath: relPath, Start: line, End: line}

	result, err := blame.Run(repo, start, lr)
	if err != nil {
		return err
	}

	fmt.Printf("%s (%.0f%% confidence)\n", result.Author, result.Confidence*100)
	fmt.Printf("  commit %s\n", result.CommitHash)
	fmt.Printf("  date   %s\n", result.Date.Format("2006-01-02"))
	fmt.Printf("  line   %s:%d\n", relPath, lr.Start)
	return nil
}

// repoRelPath converts an absolute filesystem path into a slash-separated path
// relative to the repository root, rejecting paths that escape the working tree.
func repoRelPath(repo *gitlayer.Repo, absPath string) (string, error) {
	root, err := repo.Root()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the repository at %s", absPath, root)
	}
	return filepath.ToSlash(rel), nil
}

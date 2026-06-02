package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/stephendong/sblame/internal/blame"
	"github.com/stephendong/sblame/internal/types"
)

func main() {
	line := flag.Int("line", 0, "line number to blame")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: sblame <file> [--line N]\n\n")
		fmt.Fprintf(os.Stderr, "Semantic blame: find where code logic was genuinely authored,\n")
		fmt.Fprintf(os.Stderr, "skipping cosmetic commits.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	filePath := flag.Arg(0)

	lr := types.LineRange{
		FilePath: filePath,
		Start:    *line,
		End:      *line,
	}
	if *line == 0 {
		// No line specified; default to line 1 as a placeholder.
		lr.Start = 1
		lr.End = 1
	}

	// TODO: detect the repository root and HEAD commit automatically.
	commitHash := "HEAD"

	result, err := blame.Run(filePath, lr, commitHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sblame: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s (%.0f%% confidence)\n", result.Author, result.Confidence*100)
	fmt.Printf("  commit %s\n", result.CommitHash)
	fmt.Printf("  date   %s\n", result.Date)
}

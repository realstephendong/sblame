package blame

import "sort"

// SupportedExtensions reports the file extensions with above-baseline cosmetic
// detection, split by tier:
//
//   - rename: a structural lexer exists, so consistent-rename skips work (on top
//     of comment and whitespace detection).
//   - commentOnly: a comment style is known but no rename lexer, so comment and
//     whitespace edits are skipped but a rename reads as authored.
//
// Every extension absent from both still works at the whitespace-only baseline.
// Both slices are returned sorted.
func SupportedExtensions() (rename, commentOnly []string) {
	renameSet := map[string]bool{".go": true}
	for ext := range tsLangs {
		renameSet[ext] = true
	}
	for ext := range renameSet {
		rename = append(rename, ext)
	}
	for ext := range extStyles {
		if !renameSet[ext] {
			commentOnly = append(commentOnly, ext)
		}
	}
	sort.Strings(rename)
	sort.Strings(commentOnly)
	return rename, commentOnly
}

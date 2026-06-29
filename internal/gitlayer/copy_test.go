package gitlayer

import "testing"

func TestFindBlock(t *testing.T) {
	hay := []string{"a", "b", "c", "d", "e"}
	tests := []struct {
		name      string
		needle    []string
		wantStart int
		wantOK    bool
	}{
		{"at start", []string{"a", "b"}, 1, true},
		{"in middle", []string{"c", "d"}, 3, true},
		{"at end", []string{"d", "e"}, 4, true},
		{"single line", []string{"c"}, 3, true},
		{"whole hay", []string{"a", "b", "c", "d", "e"}, 1, true},
		{"absent", []string{"x", "y"}, 0, false},
		{"present but not contiguous", []string{"a", "c"}, 0, false},
		{"needle longer than hay", []string{"a", "b", "c", "d", "e", "f"}, 0, false},
		{"empty needle", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, ok := FindBlock(hay, tt.needle)
			if ok != tt.wantOK || start != tt.wantStart {
				t.Errorf("FindBlock(%q) = (%d, %v), want (%d, %v)", tt.needle, start, ok, tt.wantStart, tt.wantOK)
			}
		})
	}
}

func TestFindBlock_FirstOccurrence(t *testing.T) {
	hay := []string{"x", "dup", "y", "dup", "z"}
	if start, ok := FindBlock(hay, []string{"dup"}); !ok || start != 2 {
		t.Errorf("FindBlock returned (%d, %v), want the first occurrence at line 2", start, ok)
	}
}

func TestBlockSubstance(t *testing.T) {
	tests := []struct {
		name  string
		block []string
		want  int
	}{
		{"counts non-whitespace runes", []string{"abc"}, 3},
		{"ignores spaces and tabs", []string{"a b\tc"}, 3},
		{"whitespace only", []string{"\t   ", ""}, 0},
		{"empty", nil, 0},
		{"across lines", []string{"ab", "cde"}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BlockSubstance(tt.block); got != tt.want {
				t.Errorf("BlockSubstance(%q) = %d, want %d", tt.block, got, tt.want)
			}
		})
	}
}

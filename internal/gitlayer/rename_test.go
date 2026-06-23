package gitlayer

import "testing"

func TestSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}, 1.0},
		{"disjoint", []string{"a", "b"}, []string{"x", "y"}, 0.0},
		{"half shared", []string{"a", "b", "c", "d"}, []string{"a", "b", "x", "y"}, 0.5},
		{"both empty", nil, nil, 1.0},
		{"one empty", nil, []string{"a"}, 0.0},
		{"subset scored over the larger file", []string{"a"}, []string{"a", "b", "c", "d"}, 0.25},
		{"duplicate lines counted as a multiset", []string{"x", "x", "y"}, []string{"x", "y", "y"}, 2.0 / 3.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Similarity(tt.a, tt.b); got != tt.want {
				t.Errorf("Similarity(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			// Similarity is symmetric.
			if got := Similarity(tt.b, tt.a); got != tt.want {
				t.Errorf("Similarity(%q, %q) = %v, want %v (asymmetric)", tt.b, tt.a, got, tt.want)
			}
		})
	}
}

func TestBestRenameMatch(t *testing.T) {
	target := []string{"a", "b", "c", "d"}

	t.Run("picks the most similar candidate above threshold", func(t *testing.T) {
		cands := map[string][]string{
			"close.go": {"a", "b", "c", "x"}, // 0.75
			"far.go":   {"a", "z", "y", "w"}, // 0.25
		}
		path, score, ok := BestRenameMatch(target, cands, RenameThreshold)
		if !ok || path != "close.go" {
			t.Fatalf("got (%q, %v, %v), want close.go", path, score, ok)
		}
		if score != 0.75 {
			t.Errorf("score = %v, want 0.75", score)
		}
	})

	t.Run("no candidate clears the threshold", func(t *testing.T) {
		cands := map[string][]string{"far.go": {"a", "z", "y", "w"}} // 0.25 < 0.5
		if _, _, ok := BestRenameMatch(target, cands, RenameThreshold); ok {
			t.Error("expected no match below threshold")
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		if _, _, ok := BestRenameMatch(target, nil, RenameThreshold); ok {
			t.Error("expected no match with no candidates")
		}
	})

	t.Run("ties broken by lexicographically smaller path", func(t *testing.T) {
		cands := map[string][]string{
			"b.go": {"a", "b", "c", "d"}, // 1.0
			"a.go": {"a", "b", "c", "d"}, // 1.0
		}
		path, _, ok := BestRenameMatch(target, cands, RenameThreshold)
		if !ok || path != "a.go" {
			t.Errorf("got %q, want a.go (deterministic tie-break)", path)
		}
	})
}

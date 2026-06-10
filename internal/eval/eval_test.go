package eval

import "testing"

// TestBuiltinCases runs the curated evaluation suite as a regression gate: every
// built-in scenario must pass, so a change that breaks the cosmetic/comment
// classification or confidence scoring fails the build.
func TestBuiltinCases(t *testing.T) {
	rep := RunCases(builtinCases())
	for _, res := range rep.Results {
		if !res.Pass {
			t.Errorf("case %q failed: %s", res.Name, res.Detail)
		}
	}
	if got := rep.Accuracy(); got != 1.0 {
		t.Errorf("accuracy: got %.0f%%, want 100%%\n%s", got*100, rep)
	}
}

package types

import "testing"

func TestClassificationString(t *testing.T) {
	tests := []struct {
		name string
		c    Classification
		want string
	}{
		{"unchanged", UNCHANGED, "UNCHANGED"},
		{"cosmetic", COSMETIC, "COSMETIC"},
		{"authored", AUTHORED, "AUTHORED"},
		{"moved", MOVED, "MOVED"},
		{"unknown value", Classification(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.String(); got != tt.want {
				t.Errorf("Classification(%d).String() = %q, want %q", tt.c, got, tt.want)
			}
		})
	}
}

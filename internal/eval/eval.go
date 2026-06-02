// Package eval provides an evaluation harness for measuring sblame accuracy
// against known-good blame attributions.
package eval

import "errors"

// ErrNotImplemented is returned by stub functions that have no implementation yet.
var ErrNotImplemented = errors.New("eval: not implemented")

// Run executes the evaluation harness.
//
// This is a stub. The real implementation will compare sblame output against
// a ground-truth dataset and report precision/recall metrics.
func Run() error {
	return ErrNotImplemented
}

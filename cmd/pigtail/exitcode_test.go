package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, 0}, {errors.New("plain"), 1}, {withExitCode(4, errors.New("x")), 4},
		{fmt.Errorf("wrapped: %w", withExitCode(8, errors.New("busy"))), 8},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

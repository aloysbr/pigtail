package main

import (
	"strings"
	"testing"
)

func TestConfirmYes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"\n", true}, {"y\n", true}, {"Y\n", true}, {"yes\n", true},
		{"n\n", false}, {"no\n", false}, {"garbage\n", false}, {"", false},
		{"y", true}, {"n", false},
	}
	for _, c := range cases {
		if got := confirmYes(strings.NewReader(c.in)); got != c.want {
			t.Errorf("confirmYes(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

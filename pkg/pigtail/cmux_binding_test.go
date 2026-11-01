package pigtail

import (
	"errors"
	"fmt"
	"testing"
)

func TestNormalizeCmuxWorkspaceID(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"29664DF9-67B9-4AAF-9419-B1FD8E3A8994", "29664DF9-67B9-4AAF-9419-B1FD8E3A8994", true},
		{" 29664df9-67b9-4aaf-9419-b1fd8e3a8994\n", "29664DF9-67B9-4AAF-9419-B1FD8E3A8994", true},
		{"", "", false},
		{"workspace:4", "", false},
		{"29664DF9-67B9-4AAF-9419-B1FD8E3A899", "", false},   // one digit short
		{"29664DF9-67B9-4AAF-9419-B1FD8E3A8994X", "", false}, // trailing junk
		{"29664DF967B94AAF9419B1FD8E3A89940000", "", false},  // no dashes
		{"G9664DF9-67B9-4AAF-9419-B1FD8E3A8994", "", false},  // not hex
	}
	for _, c := range cases {
		got, err := NormalizeCmuxWorkspaceID(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizeCmuxWorkspaceID(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestValidCmuxBindingSource(t *testing.T) {
	for _, s := range []string{CmuxSourceAttach, CmuxSourceSubjectWork, CmuxSourcePtnew} {
		if !ValidCmuxBindingSource(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "manual", "ATTACH"} {
		if ValidCmuxBindingSource(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestIsCmuxBoundElsewhere(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{Code: ErrNotFound, Details: map[string]interface{}{DetailBoundElsewhere: true}}, true},
		{&APIError{Code: ErrNotFound}, false},
		{&APIError{Code: ErrInternal, Details: map[string]interface{}{DetailBoundElsewhere: true}}, false},
		{fmt.Errorf("wrap: %w", &APIError{Code: ErrNotFound, Details: map[string]interface{}{DetailBoundElsewhere: true}}), true},
		{errors.New("x"), false},
	}
	for i, c := range cases {
		if got := IsCmuxBoundElsewhere(c.err); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}

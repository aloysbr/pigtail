package main

import (
	"strings"
	"testing"
)

// -sdk-launch opens a launch the registry cannot serve without -sdk-host and
// -sdk-node: refused at start, naming the flag, never silently ignored.
func TestSDKLaunchNeedsHostAndNode(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"-sdk-launch"}, "-sdk-launch"},
		{[]string{"-sdk-host", "/h"}, "-sdk-node"},
		{[]string{"-sdk-node", "/n"}, "-sdk-host"},
		{[]string{"-sdk-host", "/h", "-sdk-node", "/n", "-sdk-launch"}, ""},
		{[]string{"-sdk-host", "/h", "-sdk-node", "/n"}, ""},
		{nil, ""},
	} {
		code, _, errOut := checkArgsOut(tc.args)
		if tc.wantErr == "" && code != 0 {
			t.Errorf("%v refused: %q", tc.args, errOut)
		}
		if tc.wantErr != "" && (code == 0 || !strings.Contains(errOut, tc.wantErr)) {
			t.Errorf("%v = %d %q, want a refusal naming %s", tc.args, code, errOut, tc.wantErr)
		}
	}
}

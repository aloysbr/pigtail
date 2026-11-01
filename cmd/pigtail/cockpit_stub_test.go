package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestCockpitStubRefuses(t *testing.T) {
	calls := startWebDaemon(t, pigtail.WebLoginCode{Code: "x", HTTPAddr: "127.0.0.1:7811"})
	cmd := retiredWebAliasCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(nil)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "pigtail web") {
		t.Fatalf("err = %v, want a refusal naming pigtail web", err)
	}
	if out.Len() != 0 || *calls != 0 {
		t.Fatalf("stub printed %q and minted %d codes, want nothing", out.String(), *calls)
	}
	if !cmd.Hidden {
		t.Fatal("cockpit stub must be hidden from help and completion")
	}
}

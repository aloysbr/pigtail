package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestPrintPortChanges(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printPortChanges(&b, pigtail.PortsModeResync, &pigtail.PatchPortsResponse{
		WorkspaceID: "ws_efb2e9", Changed: []pigtail.PortChange{{Kind: "dev", From: 3000, To: 3013}},
	})
	if got := b.String(); !strings.Contains(got, "ws_efb2e9 ports resynced from .env.local") || !strings.Contains(got, "dev  3000 → 3013") {
		t.Fatalf("got %q", got)
	}
	b.Reset()
	printPortChanges(&b, pigtail.PortsModeResync, &pigtail.PatchPortsResponse{WorkspaceID: "ws_x"})
	if !strings.Contains(b.String(), "already match") {
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	printPortChanges(&b, pigtail.PortsModeReassign, &pigtail.PatchPortsResponse{
		WorkspaceID: "ws_392f9b", Changed: []pigtail.PortChange{{Kind: "dev", From: 3013, To: 3027}},
	})
	if !strings.Contains(b.String(), "restart its server: pigtail dev") {
		t.Fatalf("got %q", b.String())
	}
}

func TestPortsCmdShape(t *testing.T) {
	t.Parallel()
	c := portsCmd()
	names := map[string]bool{}
	for _, s := range c.Commands() {
		names[s.Name()] = true
	}
	if !names["resync"] || !names["reassign"] {
		t.Fatalf("subcommands %v", names)
	}
	re, _, _ := c.Find([]string{"reassign"})
	if re.Flags().Lookup("kind") == nil {
		t.Fatal("reassign needs --kind")
	}
}

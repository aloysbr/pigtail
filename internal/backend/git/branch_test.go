package git

import (
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestDeriveBranch(t *testing.T) {
	p := &pigtail.Project{RootPath: "/c/monorepo"}
	if got := DeriveBranch(p, "Spawn into another cell", ""); got != "feat/spawn-into-another-cell" {
		t.Fatalf("default prefix = %q", got)
	}
	if got := DeriveBranch(p, "feat: thing", ""); got != "feat/thing" {
		t.Fatalf("dedupe = %q", got)
	}
	if got := DeriveBranch(p, "whatever", "fix/x"); got != "fix/x" {
		t.Fatalf("override = %q", got)
	}
	p.Defaults = &pigtail.ProjectDefaults{BranchPrefix: "research/"}
	if got := DeriveBranch(p, "a b", ""); got != "research/a-b" {
		t.Fatalf("project prefix = %q", got)
	}
	if got := WorktreePathFor(p, "feat/a-b"); got != "/c/monorepo/feat-a-b" {
		t.Fatalf("path = %q", got)
	}
	if got := MainWorktreePath(p); got != "/c/monorepo/main" {
		t.Fatalf("main = %q", got)
	}
}

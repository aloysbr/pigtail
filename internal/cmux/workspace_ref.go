package cmux

import (
	"fmt"
	"regexp"
	"strings"
)

var workspaceRefLineRe = regexp.MustCompile(`^\s*\*?\s*(workspace:\d+)\s+([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})\b`)

// ParseWorkspaceRefs decodes `cmux workspace list --id-format both` into
// `workspace:N` ref → workspace UUID (upper case, the form the subject ↔
// workspace binding keys on).
func ParseWorkspaceRefs(out string) map[string]string {
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if m := workspaceRefLineRe.FindStringSubmatch(line); m != nil {
			refs[m[1]] = strings.ToUpper(m[2])
		}
	}
	return refs
}

// ResolveWorkspaceRef turns a `workspace:N` ref (what `cmux new-workspace`
// prints) into the workspace UUID, through `cmux workspace list`. The list
// is the calling window's: a ref from another window is not found.
func ResolveWorkspaceRef(ref string) (string, error) {
	return resolveWorkspaceRef(ref, WorkspaceList)
}

func resolveWorkspaceRef(ref string, list func() (string, error)) (string, error) {
	out, err := list()
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	id, ok := ParseWorkspaceRefs(out)[ref]
	if !ok {
		return "", fmt.Errorf("resolve %s: no such workspace in `cmux workspace list`", ref)
	}
	return id, nil
}

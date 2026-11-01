package cmux

import (
	"encoding/json"
	"errors"
	"fmt"
)

// WorkspaceKnown asks cmux whether it knows a workspace UUID, through
// `cmux identify --workspace <id>`. It answers (true, nil) when cmux places
// the workspace (caller.workspace_ref is a non-empty string), (false, nil)
// when the reply is valid JSON whose "caller" key is null (measured on cmux
// 0.65 for an unknown id), and (false, err) in every other case: the caller
// treats an error as "cannot tell" and fails open.
func WorkspaceKnown(id string) (bool, error) {
	if err := checkUUID("workspace", id); err != nil {
		return false, err
	}
	out, err := run("identify", "--workspace", id)
	if err != nil {
		return false, err
	}
	return parseWorkspaceKnown(out)
}

// parseWorkspaceKnown reads `cmux identify` output. It decodes into raw
// messages to tell an absent "caller" key (an unexpected reply: error) from a
// null one (cmux does not know the workspace).
func parseWorkspaceKnown(out []byte) (bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		return false, fmt.Errorf("cmux identify: unreadable reply: %w", err)
	}
	raw, ok := top["caller"]
	if !ok {
		return false, errors.New("cmux identify: reply has no caller key")
	}
	var caller *struct {
		WorkspaceRef *string `json:"workspace_ref"`
	}
	if err := json.Unmarshal(raw, &caller); err != nil {
		return false, fmt.Errorf("cmux identify: unreadable caller: %w", err)
	}
	if caller == nil {
		return false, nil
	}
	if caller.WorkspaceRef == nil || *caller.WorkspaceRef == "" {
		return false, errors.New("cmux identify: caller has no workspace_ref")
	}
	return true, nil
}

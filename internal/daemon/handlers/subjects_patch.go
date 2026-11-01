package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// subjectReadOnlyKeys are the Subject JSON keys a caller may send (a client
// echoing back a subject it read does) but never writes: the server owns them.
var subjectReadOnlyKeys = map[string]bool{
	"id": true, "scope": true, "created_at": true, "updated_at": true,
	// The subject's own space: written by a hand drag and cleared on a theme
	// change only (D5, D8 of sub_x000000000000033), never by a POST.
	"space": true,
	// Joined in by GET /subjects/:id; written through /cmux-workspaces.
	"cmux_workspaces": true,
	// Computed by GET /subjects/:id from the dependencies (D30).
	"blocked_by": true, "blocked_by_all": true, "blocks": true, "blocks_all": true,
}

// mergeSubjectPatch applies a partial update body onto existing and returns
// the merged subject. Only the keys present in body are written:
//
//   - absent key: the field keeps its current value — a client that does not
//     know a field (an older binary, a skill's curl) cannot erase it;
//   - null: the field is reset to its zero value (stored as NULL);
//   - any other value: written, with the Subject field's JSON type.
//
// Read-only keys are ignored. A null or blank title, an unknown key, a
// mistyped value, a body that is not a JSON object or one with nothing to
// write is an error — nothing is written then. The merge goes through the Subject JSON encoding, so a field
// added to pigtail.Subject is covered without touching this function.
func mergeSubjectPatch(existing pigtail.Subject, body []byte) (pigtail.Subject, error) {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil {
		return pigtail.Subject{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if patch == nil {
		return pigtail.Subject{}, errors.New("invalid JSON: body must be an object")
	}
	cur, err := json.Marshal(existing)
	if err != nil {
		return pigtail.Subject{}, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(cur, &merged); err != nil {
		return pigtail.Subject{}, err
	}
	writable := 0
	for k, v := range patch {
		if subjectReadOnlyKeys[k] {
			continue
		}
		writable++
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			// Dropping the key decodes to the zero value below.
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	if writable == 0 {
		return pigtail.Subject{}, errors.New("nothing to update: the body names no writable subject field")
	}
	buf, err := json.Marshal(merged)
	if err != nil {
		return pigtail.Subject{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	var out pigtail.Subject
	if err := dec.Decode(&out); err != nil {
		return pigtail.Subject{}, fmt.Errorf("invalid subject field: %w", err)
	}
	// A subject always has a title: sending it null or blank is refused
	// rather than reset. Checked only when sent, so a legacy row with an
	// empty title stays updatable.
	if _, sent := patch["title"]; sent && strings.TrimSpace(out.Title) == "" {
		return pigtail.Subject{}, errors.New("title cannot be empty")
	}
	return out, nil
}

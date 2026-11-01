package main

import (
	"encoding/json"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// subjectPatch builds the partial-update body for `subject set`: the Subject
// JSON value of each key the user named, and only those — the daemon leaves
// every other field alone. A named key whose value is now empty (dropped by
// omitempty) is sent as null, so `set board_card=` still resets it.
func subjectPatch(sub pigtail.Subject, keys []string) (map[string]any, error) {
	raw, err := json.Marshal(sub)
	if err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if k == "theme" {
			k = "theme_id"
		}
		if v, ok := all[k]; ok {
			out[k] = v
		} else {
			out[k] = nil
		}
	}
	return out, nil
}

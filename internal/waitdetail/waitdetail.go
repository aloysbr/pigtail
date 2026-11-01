// Package waitdetail validates the typed content of a wait (sub_x000000000000026,
// spec § 1): which fields each wait type takes, their bounds, and that no
// string carries a secret. Pure: no I/O, no clock.
package waitdetail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Bounds of a content (technical choices).
const (
	MaxBytes   = 16 << 10 // the JSON content
	MaxOptions = 10
	MaxItems   = 30
	MaxShots   = 10 // per level: the wait's, each item's
)

// FieldError is a refusal naming the field at fault.
type FieldError struct {
	Field string // "options[1].reco"; "" for the content as a whole
	Msg   string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return "details: " + e.Msg
	}
	return "details." + e.Field + ": " + e.Msg
}

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Field groups beyond the common ones (title, summary, step, url, shots).
const (
	fOptions = "options"
	fWhy     = "why"
	fOther   = "other"
	fItems   = "items"
	fLead    = "lead"
	fCmd     = "cmd"
	fWhere   = "where"
)

// allowed lists, per type, the fields it takes beyond the common ones.
var allowed = map[waittype.Type][]string{
	waittype.Arbitrage:  {fOptions, fWhy},
	waittype.Question:   {fOptions, fOther},
	waittype.Review:     {fItems, fLead},
	waittype.Permission: {fCmd, fWhere},
	waittype.Blocked:    nil,
	waittype.Done:       nil,
	waittype.Working:    nil,
	waittype.Wrapped:    nil,
}

// Validate decodes raw as the content of a wait of type t and checks it.
// An empty raw is no content: nil, nil. Any refusal is a *FieldError.
func Validate(t waittype.Type, raw []byte) (*pigtail.WaitDetails, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxBytes {
		return nil, fieldErr("", "content is %d bytes, the limit is %d", len(raw), MaxBytes)
	}
	extra, ok := allowed[t]
	if !ok {
		return nil, fieldErr("", "a %q wait takes no details", string(t))
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fieldErr("", "content must be a JSON object")
	}
	var d pigtail.WaitDetails
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, decodeErr(err)
	}
	if dec.More() {
		return nil, fieldErr("", "trailing data after the JSON object")
	}
	if err := checkEnvelope(&d, t); err != nil {
		return nil, err
	}
	if err := checkTypeFields(&d, extra); err != nil {
		return nil, err
	}
	if err := checkCommon(&d); err != nil {
		return nil, err
	}
	var err error
	switch t {
	case waittype.Arbitrage:
		err = checkArbitrage(&d)
	case waittype.Question:
		err = checkQuestion(&d)
	case waittype.Review:
		err = checkReview(&d)
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// checkEnvelope refuses a kind that is not the wait's type or a schema this
// pigtaild does not read, then normalises an absent envelope (D28 c).
func checkEnvelope(d *pigtail.WaitDetails, t waittype.Type) error {
	if d.Kind != "" && d.Kind != string(t) {
		return fieldErr("kind", "%q does not match the wait type %q", d.Kind, string(t))
	}
	if d.Schema != 0 && d.Schema != pigtail.WaitDetailsSchema {
		return fieldErr("schema", "unsupported version %d (this pigtaild reads %d)", d.Schema, pigtail.WaitDetailsSchema)
	}
	d.Normalize(string(t))
	return nil
}

// decodeErr names the field an encoding/json error points at.
func decodeErr(err error) error {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, "json: unknown field "); ok {
		return fieldErr(strings.Trim(rest, `"`), "unknown field")
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return fieldErr(te.Field, "expected %s, got %s", te.Type, te.Value)
	}
	return fieldErr("", "invalid JSON: %s", strings.TrimPrefix(msg, "json: "))
}

// checkTypeFields refuses a type field t does not take.
func checkTypeFields(d *pigtail.WaitDetails, extra []string) error {
	present := map[string]bool{
		fOptions: len(d.Options) > 0,
		fWhy:     d.Why != "",
		fOther:   d.Other != "",
		fItems:   len(d.Items) > 0,
		fLead:    d.Lead != "",
		fCmd:     d.Cmd != "",
		fWhere:   d.Where != "",
	}
	ok := map[string]bool{}
	for _, f := range extra {
		ok[f] = true
	}
	for _, f := range []string{fOptions, fWhy, fOther, fItems, fLead, fCmd, fWhere} {
		if present[f] && !ok[f] {
			return fieldErr(f, "not taken by this wait type")
		}
	}
	return nil
}

// checkCommon checks the bounds, the URLs and the secrets of every field.
func checkCommon(d *pigtail.WaitDetails) error {
	if len(d.Options) > MaxOptions {
		return fieldErr(fOptions, "%d options, the limit is %d", len(d.Options), MaxOptions)
	}
	if len(d.Items) > MaxItems {
		return fieldErr(fItems, "%d items, the limit is %d", len(d.Items), MaxItems)
	}
	if err := checkStrings("", map[string]string{
		"title": d.Title, "summary": d.Summary, "step": d.Step, "url": d.URL,
		"why": d.Why, "other": d.Other, "lead": d.Lead, "cmd": d.Cmd, "where": d.Where,
	}); err != nil {
		return err
	}
	if err := checkURL("url", d.URL); err != nil {
		return err
	}
	if err := checkShots("shots", d.Shots); err != nil {
		return err
	}
	for i, o := range d.Options {
		p := fmt.Sprintf("options[%d].", i)
		if err := checkStrings(p, map[string]string{"id": o.ID, "label": o.Label, "detail": o.Detail, "url": o.URL}); err != nil {
			return err
		}
		if err := checkURL(p+"url", o.URL); err != nil {
			return err
		}
	}
	for i, it := range d.Items {
		p := fmt.Sprintf("items[%d].", i)
		if err := checkStrings(p, map[string]string{"label": it.Label, "detail": it.Detail, "url": it.URL}); err != nil {
			return err
		}
		if err := checkURL(p+"url", it.URL); err != nil {
			return err
		}
		if err := checkShots(p+"shots", it.Shots); err != nil {
			return err
		}
	}
	return nil
}

// stringOrder keeps the refusal stable when several strings are at fault.
var stringOrder = []string{"id", "title", "label", "summary", "step", "detail", "url", "why", "other", "lead", "cmd", "where", "src", "caption"}

func checkStrings(prefix string, fields map[string]string) error {
	for _, k := range stringOrder {
		v, ok := fields[k]
		if !ok || v == "" {
			continue
		}
		if name := relay.SecretShape(v); name != "" {
			return fieldErr(prefix+k, "looks like it carries a secret (%s) — never put credentials in a wait", name)
		}
	}
	return nil
}

func checkShots(field string, shots []pigtail.WaitShot) error {
	if len(shots) > MaxShots {
		return fieldErr(field, "%d shots, the limit is %d", len(shots), MaxShots)
	}
	for i, s := range shots {
		p := fmt.Sprintf("%s[%d].", field, i)
		if err := checkStrings(p, map[string]string{"src": s.Src, "caption": s.Caption}); err != nil {
			return err
		}
		if s.Src == "" {
			return fieldErr(p+"src", "required")
		}
		if err := checkURL(p+"src", s.Src); err != nil {
			return err
		}
	}
	return nil
}

// checkURL accepts an http(s):// or file:// URL, or an absolute path.
func checkURL(field, u string) error {
	if u == "" {
		return nil
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") ||
		strings.HasPrefix(u, "file://") || strings.HasPrefix(u, "/") {
		return nil
	}
	return fieldErr(field, "must be an http(s):// or file:// URL, or an absolute path")
}

func checkArbitrage(d *pigtail.WaitDetails) error {
	if len(d.Options) < 2 {
		return fieldErr(fOptions, "an arbitrage lays out at least 2 options")
	}
	if err := checkOptionIDs(d.Options); err != nil {
		return err
	}
	reco := -1
	for i, o := range d.Options {
		if o.Reco {
			if reco >= 0 {
				return fieldErr(fmt.Sprintf("options[%d].reco", i), "only one reco (options[%d] is already)", reco)
			}
			reco = i
		}
	}
	if reco < 0 {
		return fieldErr(fOptions, "exactly one option carries the reco")
	}
	if strings.TrimSpace(d.Why) == "" {
		return fieldErr(fWhy, "required: why the reco")
	}
	return nil
}

// checkOptionIDs requires every option an id, unique among the options: the
// answer names its choice by id.
func checkOptionIDs(opts []pigtail.WaitOption) error {
	seen := map[string]bool{}
	for i, o := range opts {
		if strings.TrimSpace(o.ID) == "" {
			return fieldErr(fmt.Sprintf("options[%d].id", i), "required")
		}
		if seen[o.ID] {
			return fieldErr(fmt.Sprintf("options[%d].id", i), "duplicate id %q", o.ID)
		}
		seen[o.ID] = true
	}
	return nil
}

func checkQuestion(d *pigtail.WaitDetails) error {
	if err := checkOptionIDs(d.Options); err != nil {
		return err
	}
	for i, o := range d.Options {
		if o.Reco {
			return fieldErr(fmt.Sprintf("options[%d].reco", i), "a question carries no reco")
		}
	}
	return nil
}

func checkReview(d *pigtail.WaitDetails) error {
	hasItems := len(d.Items) > 0
	hasLink := d.URL != "" && d.Lead != ""
	switch {
	case hasItems && d.Lead != "":
		return fieldErr(fItems, "items or url+lead, not both")
	case !hasItems && !hasLink:
		if d.Lead != "" {
			return fieldErr(fLead, "a lead goes with a url")
		}
		return fieldErr(fItems, "a review takes items or url+lead")
	}
	return nil
}

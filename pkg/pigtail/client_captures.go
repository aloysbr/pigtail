package pigtail

import (
	"context"
	"net/url"
)

// CaptureQuery narrows Captures (GET /captures).
type CaptureQuery struct {
	Theme string
	Open  bool
	Scope string
}

// AddCapture records a capture under a theme (POST /captures). fromSubject
// ("" = none) is the subject the capture replaces (D2).
func (c *Client) AddCapture(ctx context.Context, themeID, text, fromSubject string) (*Capture, error) {
	body := map[string]string{"theme_id": themeID, "text": text}
	if fromSubject != "" {
		body["from_subject_id"] = fromSubject
	}
	var out Capture
	if _, err := c.do(ctx, "POST", "/captures", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Captures lists the captures the caller's scope sees (GET /captures).
func (c *Client) Captures(ctx context.Context, cq CaptureQuery) ([]Capture, error) {
	q := url.Values{}
	if cq.Theme != "" {
		q.Set("theme", cq.Theme)
	}
	if cq.Open {
		q.Set("open", "1")
	}
	if cq.Scope != "" {
		q.Set("scope", cq.Scope)
	}
	path := "/captures"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []Capture
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CapturePromotion is the answer of POST /captures/promote.
type CapturePromotion struct {
	Subject  Subject   `json:"subject"`
	Captures []Capture `json:"captures"`
}

// PromoteCaptures turns open captures of one theme into a subject (POST /captures/promote).
func (c *Client) PromoteCaptures(ctx context.Context, ids []string, title, shortTitle string) (*CapturePromotion, error) {
	body := map[string]any{"capture_ids": ids, "title": title}
	if shortTitle != "" {
		body["short_title"] = shortTitle
	}
	var out CapturePromotion
	if _, err := c.do(ctx, "POST", "/captures/promote", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CaptureDeletion is the answer of DELETE /captures/{id}: the capture, the
// subject it gave back its status to, or a note when that subject changed.
type CaptureDeletion struct {
	Capture Capture  `json:"capture"`
	Subject *Subject `json:"subject,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// DeleteCapture removes a capture and, when it replaced a subject still
// captured, restores that subject's previous status (DELETE /captures/{id}).
func (c *Client) DeleteCapture(ctx context.Context, id string) (*CaptureDeletion, error) {
	var out CaptureDeletion
	if _, err := c.do(ctx, "DELETE", "/captures/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

package pigtail

import (
	"context"
	"net/url"
	"strconv"
)

// AddSubjectOutput registers an output on its subject (POST
// /subjects/{id}/outputs). Re-adding the same key is a no-op: Created=false
// and the stored row, unchanged.
func (c *Client) AddSubjectOutput(ctx context.Context, o SubjectOutput) (*SubjectOutputAddResult, error) {
	var out SubjectOutputAddResult
	if _, err := c.do(ctx, "POST", "/subjects/"+url.PathEscape(o.SubjectID)+"/outputs", o, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SubjectOutputs lists a subject's outputs, seq ASC, purged ones included.
func (c *Client) SubjectOutputs(ctx context.Context, subjectID string, q SubjectOutputQuery) ([]SubjectOutput, error) {
	v := url.Values{}
	if q.SinceSeq > 0 {
		v.Set("since", strconv.FormatInt(q.SinceSeq, 10))
	}
	if q.Kind != "" {
		v.Set("kind", q.Kind)
	}
	if q.SessionID != "" {
		v.Set("session", q.SessionID)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/subjects/" + url.PathEscape(subjectID) + "/outputs"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out []SubjectOutput
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PurgeSubjectOutput marks the output of key as purged (the row stays).
func (c *Client) PurgeSubjectOutput(ctx context.Context, subjectID, key string) (*SubjectOutput, error) {
	var out SubjectOutput
	if _, err := c.do(ctx, "POST", "/subjects/"+url.PathEscape(subjectID)+"/outputs/purge", SubjectOutputPurgeRequest{Key: key}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

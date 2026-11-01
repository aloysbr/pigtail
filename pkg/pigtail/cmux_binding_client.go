package pigtail

import (
	"context"
	"net/url"
)

// AttachCmuxWorkspace binds a cmux workspace to a subject
// (POST /subjects/:id/cmux-workspaces). A workspace bound to another subject
// is an APIError with Code ErrConflict unless req.Replace is set and the
// caller may write that subject.
func (c *Client) AttachCmuxWorkspace(ctx context.Context, subjectID string, req AttachCmuxWorkspaceRequest) (*SubjectCmuxBinding, error) {
	var out SubjectCmuxBinding
	if _, err := c.do(ctx, "POST", "/subjects/"+url.PathEscape(subjectID)+"/cmux-workspaces", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CmuxWorkspaces lists the cmux workspaces bound to a subject.
func (c *Client) CmuxWorkspaces(ctx context.Context, subjectID string) ([]SubjectCmuxBinding, error) {
	var out []SubjectCmuxBinding
	if _, err := c.do(ctx, "GET", "/subjects/"+url.PathEscape(subjectID)+"/cmux-workspaces", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DetachCmuxWorkspace unbinds a cmux workspace from a subject.
func (c *Client) DetachCmuxWorkspace(ctx context.Context, subjectID, workspaceID string) error {
	_, err := c.do(ctx, "DELETE", "/subjects/"+url.PathEscape(subjectID)+"/cmux-workspaces/"+url.PathEscape(workspaceID), nil, nil)
	return err
}

// CmuxWorkspace returns the binding of a cmux workspace and its subject, or
// an APIError with Code ErrNotFound when no subject the caller may read is
// bound to it.
func (c *Client) CmuxWorkspace(ctx context.Context, workspaceID string) (*CmuxWorkspaceLookup, error) {
	var out CmuxWorkspaceLookup
	if _, err := c.do(ctx, "GET", "/cmux-workspaces/"+url.PathEscape(workspaceID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CmuxWorkspaceState returns the state of a cmux workspace (bound, off-topic, unbound).
func (c *Client) CmuxWorkspaceState(ctx context.Context, workspaceID string) (*CmuxWorkspaceState, error) {
	var out CmuxWorkspaceState
	if _, err := c.do(ctx, "GET", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/state", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeclareOffTopic declares a cmux workspace off-topic.
func (c *Client) DeclareOffTopic(ctx context.Context, workspaceID string, req DeclareOffTopicRequest) (*WorkspaceOffTopic, error) {
	var out WorkspaceOffTopic
	if _, err := c.do(ctx, "POST", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/off-topic", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LiftOffTopic lifts the off-topic declaration of a cmux workspace; a non-empty
// ifSession lifts it only when that session declared it (404 otherwise).
func (c *Client) LiftOffTopic(ctx context.Context, workspaceID, ifSession string) error {
	path := "/cmux-workspaces/" + url.PathEscape(workspaceID) + "/off-topic"
	if ifSession != "" {
		path += "?session_id=" + url.QueryEscape(ifSession)
	}
	_, err := c.do(ctx, "DELETE", path, nil, nil)
	return err
}

// SetOffTopicSpace rewrites only the space of an off-topic row (master only,
// PATCH /cmux-workspaces/{wsid}/off-topic/space); "" lets the daemon infer it.
func (c *Client) SetOffTopicSpace(ctx context.Context, workspaceID, space string) (*WorkspaceOffTopic, error) {
	var out WorkspaceOffTopic
	if _, err := c.do(ctx, "PATCH", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/off-topic/space", map[string]string{"space": space}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOffTopic lists the off-topic declarations the caller may read.
func (c *Client) ListOffTopic(ctx context.Context) ([]WorkspaceOffTopic, error) {
	var out []WorkspaceOffTopic
	if _, err := c.do(ctx, "GET", "/off-topic-workspaces", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

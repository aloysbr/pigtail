package pigtail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSocketPath = "~/.config/pigtail/pigtail.sock"
	DefaultHTTPAddr   = "127.0.0.1:7800"
	APIVersion        = "v1"
)

// Client talks to the pigtaild daemon over Unix socket or HTTP.
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	scope      string // X-Pigtail-Scope; "" sends none (the daemon reads its default scope)
	crossScope bool   // X-Pigtail-Cross-Scope: write
	session    string // X-Pigtail-Session; "" sends none
}

// WithSession returns a copy of c that names the Claude session it runs for
// on every request (X-Pigtail-Session). Only the maintenance freeze reads it,
// to let the exempt session through.
func (c *Client) WithSession(id string) *Client {
	cp := *c
	cp.session = id
	return &cp
}

// WithScope returns a copy of c that speaks for the given scope (globex,
// perso) on every request. The daemon enforces what that scope may see.
func (c *Client) WithScope(scope string) *Client {
	cp := *c
	cp.scope = scope
	return &cp
}

// WithCrossScopeWrite returns a copy of c that opts into writing objects of
// another scope (a perso caller writing globex). It never widens a globex caller.
func (c *Client) WithCrossScopeWrite() *Client {
	cp := *c
	cp.crossScope = true
	return &cp
}

// NewSocketClient returns a Client that connects via the Unix domain socket.
func NewSocketClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = expandHome(DefaultSocketPath)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		httpClient: &http.Client{Transport: transport},
		baseURL:    "http://pigtail",
	}
}

// NewHTTPClient returns a Client that connects via HTTP loopback with a bearer token.
func NewHTTPClient(addr, token string) *Client {
	if addr == "" {
		addr = DefaultHTTPAddr
	}
	return &Client{
		httpClient: http.DefaultClient,
		baseURL:    "http://" + addr,
		token:      token,
	}
}

// Health checks daemon liveness.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, "GET", "/health", nil, nil)
	return err
}

// Dispatch calls POST /dispatch and returns the created workspace + agent IDs.
func (c *Client) Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResponse, error) {
	var out DispatchResponse
	if _, err := c.do(ctx, "POST", "/dispatch", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RunAgent calls POST /workspaces/{id}/run to spawn an additional agent
// session inside an existing workspace (no new worktree).
func (c *Client) RunAgent(ctx context.Context, workspaceID string, req RunRequest) (*RunResponse, error) {
	var out RunResponse
	if _, err := c.do(ctx, "POST", "/workspaces/"+workspaceID+"/run", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// New calls POST /workspaces/new and returns the created workspace (no agent).
func (c *Client) New(ctx context.Context, req NewWorkspaceRequest) (*Workspace, error) {
	var out Workspace
	if _, err := c.do(ctx, "POST", "/workspaces/new", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Workspaces calls GET /workspaces and returns the list.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var out []Workspace
	if _, err := c.do(ctx, "GET", "/workspaces", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Workspace calls GET /workspaces/{id} and returns the workspace. An unknown id
// is an *APIError with Code ErrWorkspaceNotFound.
func (c *Client) Workspace(ctx context.Context, id string) (*Workspace, error) {
	var out Workspace
	if _, err := c.do(ctx, "GET", "/workspaces/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AdoptWorkspace calls POST /workspaces to register a pre-existing worktree.
func (c *Client) AdoptWorkspace(ctx context.Context, ws Workspace) (*Workspace, error) {
	var out Workspace
	if _, err := c.do(ctx, "POST", "/workspaces", ws, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplyRequest is the body of POST /agents/:id/reply.
type ReplyRequest struct {
	Content string `json:"content"`
}

// ReplyAgent calls POST /agents/:id/reply, forwarding user input to a
// running agent.
func (c *Client) ReplyAgent(ctx context.Context, agentSessionID string, req ReplyRequest) error {
	_, err := c.do(ctx, "POST", "/agents/"+agentSessionID+"/reply", req, nil)
	return err
}

// CleanupRequest is the body of DELETE /workspaces/:id.
type CleanupRequest struct {
	Force      bool `json:"force,omitempty"`
	KeepBranch bool `json:"keep_branch,omitempty"`
}

// CleanupResponse reports what the teardown actually did. The branch fields
// are populated only for outcomes the user would not otherwise predict — see
// backend.CleanupOutcome.
type CleanupResponse struct {
	WorkspaceID      string `json:"workspace_id"`
	BranchDeleted    string `json:"branch_deleted,omitempty"`
	BranchKept       string `json:"branch_kept,omitempty"`
	BranchKeptReason string `json:"branch_kept_reason,omitempty"`
	// ServersStopped lists the dev servers stopped before the teardown.
	ServersStopped []StoppedServer `json:"servers_stopped,omitempty"`
}

// StoppedServer is one dev server the teardown stopped before removing the
// workspace: the port kinds that share the port, the port, and every pid
// signalled (the listener and its descendants).
type StoppedServer struct {
	Kinds []string `json:"kinds"`
	Port  int      `json:"port"`
	PIDs  []int    `json:"pids"`
}

// CleanupWorkspace calls DELETE /workspaces/:id.
//
// The response is non-nil only on success; a caller that only cares whether
// the teardown worked can ignore it.
func (c *Client) CleanupWorkspace(ctx context.Context, id string, req CleanupRequest) (*CleanupResponse, error) {
	var out CleanupResponse
	if _, err := c.do(ctx, "DELETE", "/workspaces/"+id, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenameWorkspace calls POST /workspaces/:id/rename.
func (c *Client) RenameWorkspace(ctx context.Context, id string, req RenameRequest) (*RenameResponse, error) {
	var out RenameResponse
	if _, err := c.do(ctx, "POST", "/workspaces/"+id+"/rename", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RunWorkspaceHooks calls POST /workspaces/{id}/hooks/run: re-runs the
// workspace's post-create hooks and answers it with the recorded outcome.
func (c *Client) RunWorkspaceHooks(ctx context.Context, id string) (*Workspace, error) {
	var out Workspace
	if _, err := c.do(ctx, "POST", "/workspaces/"+id+"/hooks/run", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchWorkspacePorts calls PATCH /workspaces/{id}/ports (resync | reassign).
func (c *Client) PatchWorkspacePorts(ctx context.Context, id string, req PatchPortsRequest) (*PatchPortsResponse, error) {
	var out PatchPortsResponse
	if _, err := c.do(ctx, "PATCH", "/workspaces/"+id+"/ports", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GCWorkspaces calls POST /workspaces/gc, deleting workspace rows whose
// worktree directory no longer exists (or, with dryRun, just listing them).
func (c *Client) GCWorkspaces(ctx context.Context, dryRun bool) (*GCResponse, error) {
	var out GCResponse
	if _, err := c.do(ctx, "POST", "/workspaces/gc", GCRequest{DryRun: dryRun}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- static servers -------------------------------------------------------

// CreateStaticServerRequest is the body of POST /static-servers. Port is
// omitted from the JSON (so the daemon auto-allocates) when zero.
type CreateStaticServerRequest struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	Port int    `json:"port,omitempty"`
}

// CreateStaticServer registers a named static file server (POST /static-servers).
// Pass port=0 to let the daemon auto-allocate. The daemon mints the id and
// resolves/validates dir; callers should pass an absolute dir.
func (c *Client) CreateStaticServer(ctx context.Context, name, dir string, port int) (*StaticServer, error) {
	var out StaticServer
	req := CreateStaticServerRequest{Name: name, Dir: dir, Port: port}
	if _, err := c.do(ctx, "POST", "/static-servers", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListStaticServers lists static servers with live running state (GET /static-servers).
func (c *Client) ListStaticServers(ctx context.Context) ([]StaticServerView, error) {
	var out []StaticServerView
	if _, err := c.do(ctx, "GET", "/static-servers", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StartStaticServer starts a static server by name (POST /static-servers/{name}/start).
func (c *Client) StartStaticServer(ctx context.Context, name string) (*StaticServerView, error) {
	var out StaticServerView
	if _, err := c.do(ctx, "POST", "/static-servers/"+name+"/start", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StopStaticServer stops a running static server by name (POST /static-servers/{name}/stop).
func (c *Client) StopStaticServer(ctx context.Context, name string) error {
	_, err := c.do(ctx, "POST", "/static-servers/"+name+"/stop", nil, nil)
	return err
}

// DeleteStaticServer removes a static server record by name (DELETE /static-servers/{name}).
func (c *Client) DeleteStaticServer(ctx context.Context, name string) error {
	_, err := c.do(ctx, "DELETE", "/static-servers/"+name, nil, nil)
	return err
}

// StopAllStaticServers stops every running static server and returns the names
// stopped (POST /static-servers/stop-all).
func (c *Client) StopAllStaticServers(ctx context.Context) ([]string, error) {
	var out struct {
		Stopped []string `json:"stopped"`
	}
	if _, err := c.do(ctx, "POST", "/static-servers/stop-all", nil, &out); err != nil {
		return nil, err
	}
	return out.Stopped, nil
}

// Servers lists running dev servers with memory/idle + system headroom (GET /servers).
func (c *Client) Servers(ctx context.Context) (ServersResponse, error) {
	var out ServersResponse
	if _, err := c.do(ctx, "GET", "/servers", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Backup reports the daemon's classification of the offsite backup
// destination (GET /backup) — the same verdict `pigtail db list` renders from
// offsite-state.json directly, read here instead because a running daemon is
// exactly what `pigtail servers` already talks to.
func (c *Client) Backup(ctx context.Context) (BackupResponse, error) {
	var out BackupResponse
	if _, err := c.do(ctx, "GET", "/backup", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// RunOffsiteBackup triggers an immediate offsite backup run (POST
// /backup/offsite/run): "I just fixed something, prove it now" instead of
// waiting for the hourly primary rotation or the six-hourly fallback
// exercise. Starts both routes — the primary copy and the fallback exercise
// — under their own independent in-flight guards; the fallback's own 6h
// due-check is bypassed on this explicit trigger, its correction guards are
// not. The response reports what started, never a fresh verdict: both are
// asynchronous, so poll Backup afterward to see the result land.
func (c *Client) RunOffsiteBackup(ctx context.Context) (BackupRunResponse, error) {
	var out BackupRunResponse
	if _, err := c.do(ctx, "POST", "/backup/offsite/run", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// PinServer pins a running dev server against battery eviction for d.
// Allowed durations: 30m, 1h, 4h.
func (c *Client) PinServer(ctx context.Context, ws, server string, d time.Duration) error {
	dur := map[time.Duration]string{30 * time.Minute: "30m", time.Hour: "1h", 4 * time.Hour: "4h"}[d]
	_, err := c.do(ctx, "POST", "/servers/"+ws+"/"+server+"/pin?duration="+dur, nil, nil)
	return err
}

// UnpinServer clears a server's pin.
func (c *Client) UnpinServer(ctx context.Context, ws, server string) error {
	_, err := c.do(ctx, "DELETE", "/servers/"+ws+"/"+server+"/pin", nil, nil)
	return err
}

// StopServerByID stops one running dev server.
func (c *Client) StopServerByID(ctx context.Context, ws, server string) error {
	_, err := c.do(ctx, "POST", "/servers/"+ws+"/"+server+"/stop", nil, nil)
	return err
}

// StopAllServers stops every running dev server.
func (c *Client) StopAllServers(ctx context.Context) error {
	_, err := c.do(ctx, "POST", "/servers/stop-all", nil, nil)
	return err
}

// MarkServerStopped tells the daemon a dev server was stopped on purpose, so
// its death detector does not report the disappearance. Best-effort: callers
// ignore the error — an unreachable daemon costs one false death, never a
// missed one.
func (c *Client) MarkServerStopped(ctx context.Context, workspaceID, server string) error {
	body := map[string]string{"workspace_id": workspaceID, "server": server}
	_, err := c.do(ctx, "POST", "/servers/stopped", body, nil)
	return err
}

// StopIdleServers stops every dev server idle at least graceSeconds, sparing
// pinned servers, and reports every server's outcome — stopped, or the reason
// it was spared ("active" or "pinned"). Pass graceSeconds<=0 to use the
// daemon's configured idle-purge grace instead; the daemon refuses with an
// error if that is also disabled (0), rather than guessing.
func (c *Client) StopIdleServers(ctx context.Context, graceSeconds int) (StopIdleResponse, error) {
	var out StopIdleResponse
	body := map[string]int{"grace_seconds": graceSeconds}
	if _, err := c.do(ctx, "POST", "/servers/stop-idle", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Battery returns the current battery-mode status (GET /battery).
func (c *Client) Battery(ctx context.Context) (BatteryResponse, error) {
	var out BatteryResponse
	if _, err := c.do(ctx, "GET", "/battery", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// SetBattery toggles battery mode for the running daemon session (POST
// /battery) and returns the resulting status.
func (c *Client) SetBattery(ctx context.Context, enabled bool) (BatteryResponse, error) {
	var out BatteryResponse
	body := map[string]bool{"enabled": enabled}
	if _, err := c.do(ctx, "POST", "/battery", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Purge fetches the current always-on idle-purge threshold.
func (c *Client) Purge(ctx context.Context) (PurgeResponse, error) {
	var out PurgeResponse
	if _, err := c.do(ctx, "GET", "/purge", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// SetPurge sets the idle-purge threshold (seconds; 0 = off) for the running
// daemon session.
func (c *Client) SetPurge(ctx context.Context, graceSeconds int) (PurgeResponse, error) {
	var out PurgeResponse
	body := map[string]int{"grace_seconds": graceSeconds}
	if _, err := c.do(ctx, "POST", "/purge", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// CPUSlots returns the CPU slot arbiter's state (GET /cpu/slots): the budget,
// what is held, what is waiting, and each group's fair share.
func (c *Client) CPUSlots(ctx context.Context) (CPUSlotsResponse, error) {
	var out CPUSlotsResponse
	if _, err := c.do(ctx, "GET", "/cpu/slots", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// MaintenanceView is what the daemon reads from the maintenance state (GET
// /maintenance): its directory, the id and the flags — never the reason nor
// the exempt session. Unreadable carries the refusal text when the state file
// exists but cannot be read.
type MaintenanceView struct {
	Dir        string `json:"dir"`
	ID         string `json:"id,omitempty"`
	Unreadable string `json:"unreadable,omitempty"`
	Present    bool   `json:"present"`
	On         bool   `json:"on"`
	Freeze     bool   `json:"freeze"`
	Late       bool   `json:"late"`
}

// Maintenance calls GET /maintenance: which maintenance state the daemon
// reads, so the CLI can check it matches the one it just wrote.
func (c *Client) Maintenance(ctx context.Context) (*MaintenanceView, error) {
	var out MaintenanceView
	if _, err := c.do(ctx, "GET", "/maintenance", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VersionInfo is GET /version: the daemon's version, its build identity
// (branch, full commit, dirty — "unknown" when unstamped, absent from a
// daemon older than the stamp) and the APIs it speaks.
type VersionInfo struct {
	DaemonVersion string   `json:"daemon_version"`
	DaemonBranch  string   `json:"daemon_branch,omitempty"`
	DaemonCommit  string   `json:"daemon_commit,omitempty"`
	DaemonDirty   string   `json:"daemon_dirty,omitempty"`
	SupportedAPIs []string `json:"supported_apis"`
}

// VersionInfo calls GET /version.
func (c *Client) VersionInfo(ctx context.Context) (VersionInfo, error) {
	var out VersionInfo
	if _, err := c.do(ctx, "GET", "/version", nil, &out); err != nil {
		return VersionInfo{}, err
	}
	return out, nil
}

// Version calls GET /version and returns the daemon version + supported APIs.
func (c *Client) Version(ctx context.Context) (daemonVersion string, supportedAPIs []string, err error) {
	vi, err := c.VersionInfo(ctx)
	if err != nil {
		return "", nil, err
	}
	return vi.DaemonVersion, vi.SupportedAPIs, nil
}

// Preflight calls GET /workspaces/:id/preflight and returns the check summary.
func (c *Client) Preflight(ctx context.Context, wsID string) (*PreflightResponse, error) {
	var out PreflightResponse
	if _, err := c.do(ctx, "GET", "/workspaces/"+wsID+"/preflight", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Projects calls GET /projects and returns the registered projects.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out []Project
	if _, err := c.do(ctx, "GET", "/projects", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateProject registers a new project with the daemon.
func (c *Client) CreateProject(ctx context.Context, p Project) (*Project, error) {
	var out Project
	if _, err := c.do(ctx, "POST", "/projects", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateTheme registers a theme (POST /themes). The daemon mints the id.
func (c *Client) CreateTheme(ctx context.Context, th Theme) (*Theme, error) {
	var out Theme
	if _, err := c.do(ctx, "POST", "/themes", th, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Themes lists the themes the caller's scope sees (GET /themes). scope ("",
// globex, perso, all) only narrows that view.
func (c *Client) Themes(ctx context.Context, scope string) ([]Theme, error) {
	path := "/themes"
	if scope != "" {
		path += "?" + url.Values{"scope": {scope}}.Encode()
	}
	var out []Theme
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateTheme applies a partial update to a theme (PATCH /themes/{id}). Pass a
// non-nil pointer for each field to change; nil leaves it unchanged. Updates the
// DB only — moving vault prose is the caller's job.
func (c *Client) UpdateTheme(ctx context.Context, id string, u ThemeUpdate) (*Theme, error) {
	body := map[string]any{}
	if u.Name != nil {
		body["name"] = *u.Name
	}
	if u.VaultLink != nil {
		body["vault_link"] = *u.VaultLink
	}
	if u.Space != nil {
		body["space"] = *u.Space
	}
	if u.Deadline != nil {
		body["deadline"] = *u.Deadline
	}
	if u.Carried != nil {
		body["carried"] = *u.Carried
	}
	var out Theme
	if _, err := c.do(ctx, "PATCH", "/themes/"+id, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Spaces lists the spaces the caller's scope may name (GET /spaces), in folder order.
func (c *Client) Spaces(ctx context.Context) ([]Space, error) {
	var out []Space
	if _, err := c.do(ctx, "GET", "/spaces", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteTheme removes a theme (DELETE /themes/{id}). The daemon refuses with a
// CONFLICT error if any subject is still linked. Vault files are not touched.
func (c *Client) DeleteTheme(ctx context.Context, id string) error {
	_, err := c.do(ctx, "DELETE", "/themes/"+id, nil, nil)
	return err
}

// CreateSubject creates a subject (POST /subjects). The daemon mints id/slug/status.
func (c *Client) CreateSubject(ctx context.Context, sub Subject) (*Subject, error) {
	var out Subject
	if _, err := c.do(ctx, "POST", "/subjects", sub, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Subject fetches one subject (GET /subjects/{id}).
func (c *Client) Subject(ctx context.Context, id string) (*Subject, error) {
	var out Subject
	if _, err := c.do(ctx, "GET", "/subjects/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SubjectWorktree returns the live (non-terminal) worktree linked to a subject
// — a shipped/archived worktree is treated as none, so callers reuse only
// in-flight work — or an APIError with Code ErrNotFound if none.
func (c *Client) SubjectWorktree(ctx context.Context, subjectID string) (*Workspace, error) {
	var out Workspace
	if _, err := c.do(ctx, "GET", "/subjects/"+subjectID+"/worktree", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SubjectQuery holds the optional filters of GET /subjects ("" = no filter).
// Scope (globex, perso, all) only narrows what the caller's scope sees.
type SubjectQuery struct {
	Theme  string
	Status string
	Source string
	Scope  string
}

// Subjects lists subjects matching q (GET /subjects).
func (c *Client) Subjects(ctx context.Context, sq SubjectQuery) ([]Subject, error) {
	q := url.Values{}
	for k, v := range map[string]string{"theme": sq.Theme, "status": sq.Status, "source": sq.Source, "scope": sq.Scope} {
		if v != "" {
			q.Set(k, v)
		}
	}
	path := "/subjects"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []Subject
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SuggestSubjects ranks the subjects a pane's request may belong to (POST
// /subjects/suggest). A daemon without the route answers 404: see
// IsNoSuggestRoute.
func (c *Client) SuggestSubjects(ctx context.Context, req SuggestRequest) (*SuggestResult, error) {
	var out SuggestResult
	if _, err := c.do(ctx, "POST", "/subjects/suggest", req, &out); err != nil {
		return nil, err
	}
	if out.Candidates == nil {
		out.Candidates = []SuggestCandidate{}
	}
	return &out, nil
}

// FindSubjects ranks the subjects a few words name and records the search
// (POST /subjects/find). A daemon without the route answers like one without
// suggest: see IsNoSuggestRoute.
func (c *Client) FindSubjects(ctx context.Context, req SubjectFindRequest) (*SubjectFindResult, error) {
	var out SubjectFindResult
	if _, err := c.do(ctx, "POST", "/subjects/find", req, &out); err != nil {
		return nil, err
	}
	if out.Candidates == nil {
		out.Candidates = []SubjectFindCandidate{}
	}
	return &out, nil
}

// PickSubjectFind records the subject a search led to (POST
// /subject-finds/{fid}/pick). A daemon without the route answers a bare 404:
// see IsBareNoRoute.
func (c *Client) PickSubjectFind(ctx context.Context, fid, subjectID string) (*SubjectFindPick, error) {
	var out SubjectFindPick
	if _, err := c.do(ctx, "POST", "/subject-finds/"+url.PathEscape(fid)+"/pick", SubjectFindPickRequest{SubjectID: subjectID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IsBareNoRoute says whether err is a router's own 404/405, with no API
// envelope: the route does not exist on that daemon.
func IsBareNoRoute(err error) bool {
	var st *HTTPStatusError
	return errors.As(err, &st) && (st.Status == 404 || st.Status == 405)
}

// IsNoSuggestRoute says whether err is how a daemon older than POST
// /subjects/suggest answers it: the route falls on POST /subjects/{id} with
// id=suggest (404 NOT_FOUND "no such subject"), or a bare 404/405.
func IsNoSuggestRoute(err error) bool {
	var api *APIError
	if errors.As(err, &api) {
		return api.Code == ErrNotFound && api.Message == "no such subject"
	}
	var st *HTTPStatusError
	if errors.As(err, &st) {
		return st.Status == 404 || st.Status == 405
	}
	return false
}

// PatchSubject writes only the given fields of a subject (POST
// /subjects/{id}, partial update). Keys are Subject JSON names; a nil value
// or the zero value resets the field, a key left out is not touched. Returns
// the updated subject.
func (c *Client) PatchSubject(ctx context.Context, id string, fields map[string]any) (*Subject, error) {
	var out Subject
	if _, err := c.do(ctx, "POST", "/subjects/"+id, fields, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSubject writes a subject back (POST /subjects/{id}). The daemon
// applies the body as a partial update: every field sub carries is written,
// but an empty field is left out by its omitempty tag and so is not reset.
//
// Deprecated: use PatchSubject, which names the fields to write and can
// reset them.
func (c *Client) UpdateSubject(ctx context.Context, sub Subject) (*Subject, error) {
	var out Subject
	if _, err := c.do(ctx, "POST", "/subjects/"+sub.ID, sub, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NextSubject returns the next actionable subject (GET /subjects/next) of
// scope — "" for the caller's own.
func (c *Client) NextSubject(ctx context.Context, scope string) (*Subject, error) {
	path := "/subjects/next"
	if scope != "" {
		path += "?" + url.Values{"scope": {scope}}.Encode()
	}
	var out Subject
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreatePhase adds a phase to a subject (POST /subjects/{id}/phases).
func (c *Client) CreatePhase(ctx context.Context, subjectID string, ph SubjectPhase) (*SubjectPhase, error) {
	var out SubjectPhase
	if _, err := c.do(ctx, "POST", "/subjects/"+subjectID+"/phases", ph, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Phases lists a subject's phases (GET /subjects/{id}/phases).
func (c *Client) Phases(ctx context.Context, subjectID string) ([]SubjectPhase, error) {
	var out []SubjectPhase
	if _, err := c.do(ctx, "GET", "/subjects/"+subjectID+"/phases", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdatePhase sets a phase's state and workspace (POST /phases/{id}).
func (c *Client) UpdatePhase(ctx context.Context, phaseID, state, workspaceID string) error {
	body := map[string]string{"state": state, "workspace_id": workspaceID}
	_, err := c.do(ctx, "POST", "/phases/"+phaseID, body, nil)
	return err
}

// AppendLog appends a log entry to a subject (POST /subjects/{id}/log).
func (c *Client) AppendLog(ctx context.Context, subjectID string, e SubjectLogEntry) (*SubjectLogEntry, error) {
	var out SubjectLogEntry
	if _, err := c.do(ctx, "POST", "/subjects/"+subjectID+"/log", e, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Log lists a subject's log entries newest-first (GET /subjects/{id}/log).
func (c *Client) Log(ctx context.Context, subjectID string) ([]SubjectLogEntry, error) {
	var out []SubjectLogEntry
	if _, err := c.do(ctx, "GET", "/subjects/"+subjectID+"/log", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LogSince lists a subject's log entries at or after since, newest-first
// (GET /subjects/{id}/log?since=<ms>).
func (c *Client) LogSince(ctx context.Context, subjectID string, since time.Time) ([]SubjectLogEntry, error) {
	path := "/subjects/" + subjectID + "/log?since=" + strconv.FormatInt(since.UnixMilli(), 10)
	var out []SubjectLogEntry
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Events opens a GET /events SSE stream. The returned channel is closed
// when ctx is cancelled or the connection drops.
func (c *Client) Events(ctx context.Context, req EventsRequest) (<-chan Event, error) {
	path := "/events"
	if len(req.Types) > 0 {
		q := url.Values{}
		q.Set("types", strings.Join(req.Types, ","))
		path += "?" + q.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("X-Pigtail-API", APIVersion)
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.scope != "" {
		httpReq.Header.Set(HeaderScope, c.scope)
	}
	if c.session != "" {
		httpReq.Header.Set(HeaderSession, c.session)
	}
	if req.Since != "" {
		httpReq.Header.Set("Last-Event-ID", req.Since)
	}
	resp, err := c.httpClient.Do(httpReq) //nolint:bodyclose // body is closed in the streaming goroutine below
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("events: status %d: %s", resp.StatusCode, string(body))
	}
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		var dataLine string
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if dataLine != "" {
					var e Event
					if err := json.Unmarshal([]byte(dataLine), &e); err == nil {
						select {
						case out <- e:
						case <-ctx.Done():
							return
						}
					}
					dataLine = ""
				}
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				dataLine = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out, nil
}

// EventsRequest configures the GET /events subscription.
type EventsRequest struct {
	Since string   // Last-Event-ID for replay
	Types []string // filter, e.g. ["workspace.*", "agent.paused"]
}

// HTTPStatusError is an HTTP error answer without pigtail's JSON envelope
// (chi's plain-text 404 from an older daemon, a proxy page): the daemon
// answered, so it is not a transport error.
type HTTPStatusError struct {
	Status int
	Body   string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("pigtail: HTTP %d: %s", e.Status, e.Body)
}

// do is the low-level HTTP helper. Returns the response body bytes for
// callers that want to read meta info; decoded `data` is also written
// into `out` when non-nil.
func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Pigtail-API", APIVersion)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.scope != "" {
		req.Header.Set(HeaderScope, c.scope)
	}
	if c.crossScope {
		req.Header.Set(HeaderCrossScope, CrossScopeWrite)
	}
	if c.session != "" {
		req.Header.Set(HeaderSession, c.session)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var errEnv ErrorResponse
		if jsonErr := json.Unmarshal(raw, &errEnv); jsonErr == nil && errEnv.Error.Code != "" {
			return nil, &errEnv.Error
		}
		return nil, &HTTPStatusError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out != nil {
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return raw, fmt.Errorf("decode envelope: %w (body=%s)", err, string(raw))
		}
		if len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return raw, fmt.Errorf("decode data: %w", err)
			}
		}
	}
	return raw, nil
}

// shutWarn is a placeholder to silence linters when time is unused in
// minimal builds. (Kept here to keep imports stable as the client grows.)
var _ = time.Second

func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

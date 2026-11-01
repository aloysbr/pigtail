package handlers

import (
	"encoding/json"
	"errors"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// SDK sessions (docs/wire-protocol.md § SDK sessions): Claude sessions held by
// the Agent SDK. Reads follow the subject's scope like every subject route; the
// three writes that work to a session (answer, messages, end) take the web
// web front's cookie alone — never an agent's X-Pigtail-Session nor a bearer
// caller — which reverses D2 of waits_answer.go for these sessions only.

// SDKRegistry is the on-disk view of the SDK sessions (internal/sdksession).
type SDKRegistry interface {
	List(subjectID string) ([]sdksession.Session, error)
	Settle(id string) (sdksession.Session, error)
	Events(id string, after int) ([]json.RawMessage, error)
	Dial(id string, cmd any) (sdksession.HostReply, error)
}

// SDKGate runs a SDK launch as the only launch of its subject (launch.Launcher).
type SDKGate interface {
	WithSDKReservation(sub pigtail.Subject, fn func(launch.Resolved) (string, error)) (string, error)
}

// SDKService is what the SDK routes run on. A nil *SDKService is the option off.
type SDKService struct {
	Registry SDKRegistry
	Gate     SDKGate
	// LaunchEnabled: pigtaild runs with -sdk-launch.
	LaunchEnabled bool
	// Prompt is the first message of the session (settings.yaml). Nil = the neutral default.
	Prompt  func(id, title string) string
	NewID   func() string
	NewUUID func() string
	// Reserve writes meta.json (the session counts as live from then) and
	// returns the function that starts the host; the handler runs it after
	// the answer, outside the launch lock.
	Reserve func(sdksession.Meta) (begin func(), err error)
}

func (s *SDKService) prompt(id, title string) string {
	if s.Prompt != nil {
		return s.Prompt(id, title)
	}
	return usersettings.Defaults().LaunchPrompt(id, title)
}

const sdkNoSuchSession = "no such sdk session"

func registerSDKRoutes(r chi.Router, fz chi.Router, svc *Services, resp Responder) {
	fz.Post("/subjects/{id}/sdk-sessions", LaunchSDKSession(svc, resp))
	r.Get("/sdk-sessions", ListSDKSessions(svc, resp))
	r.Get("/sdk-sessions/{id}", GetSDKSession(svc, resp))
	r.Get("/sdk-sessions/{id}/events", GetSDKEvents(svc, resp))
	fz.Post("/sdk-sessions/{id}/requests/{rid}/answer", AnswerSDKRequest(svc, resp))
	fz.Post("/sdk-sessions/{id}/messages", MessageSDKSession(svc, resp))
	fz.Post("/sdk-sessions/{id}/end", EndSDKSession(svc, resp))
}

func sdkOff(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder) {
	msg := "sdk sessions are off (pigtaild runs without -sdk-host and -sdk-node)"
	if svc.SDKOffReason != "" {
		msg = "sdk sessions are off: " + svc.SDKOffReason
	}
	resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrSDKOff, msg, nil)
}

func sdkNotFound(w http.ResponseWriter, r *http.Request, resp Responder) {
	resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, sdkNoSuchSession, nil)
}

// sdkVisible reports whether the caller may see a session of subjectID: the
// subject's scope decides; a session whose subject is gone is the master's.
func sdkVisible(svc *Services, c callerScope, subjectID string, write bool) (bool, error) {
	sub, err := svc.Store.GetSubject(subjectID)
	if errors.Is(err, store.ErrNotFound) {
		return c.scope == scope.Master() && (!write || c.cross), nil
	}
	if err != nil {
		return false, err
	}
	return authorize(c, scope.Scope(sub.Scope), write) == nil, nil
}

// sdkSession loads session id the caller may read (or write); ok=false means
// the response is written, with one body for an unknown and a foreign id.
func sdkSession(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, write bool) (sdksession.Session, bool) {
	s, err := svc.SDK.Registry.Settle(chi.URLParam(r, "id"))
	if errors.Is(err, sdksession.ErrNotFound) {
		sdkNotFound(w, r, resp)
		return s, false
	}
	if err != nil {
		resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable, err.Error(), nil)
		return s, false
	}
	ok, err := sdkVisible(svc, callerOf(r), s.SubjectID, write)
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return s, false
	}
	if !ok {
		sdkNotFound(w, r, resp)
		return s, false
	}
	return s, true
}

// LaunchSDKSession handles POST /subjects/{id}/sdk-sessions: 202 once the
// session is reserved; the host starts after the answer.
func LaunchSDKSession(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		sub, ok := authorizeSubject(w, r, svc, resp, chi.URLParam(r, "id"), false)
		if !ok {
			return
		}
		if c := callerOf(r); !c.web && scope.Canonical(sub.Scope) != c.scope {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such subject", nil)
			return
		}
		if !svc.SDK.LaunchEnabled || svc.SDK.Gate == nil || svc.SDK.Reserve == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrSDKLaunchOff, "sdk sessions can be read, not launched (pigtaild runs without -sdk-launch)", nil)
			return
		}
		var begin func()
		id, err := svc.SDK.Gate.WithSDKReservation(*sub, func(res launch.Resolved) (string, error) {
			m := sdksession.Meta{
				ID: svc.SDK.NewID(), SubjectID: sub.ID, Cwd: res.Cwd, ConfigDir: res.Cell.ConfigDir,
				Mode: res.Mode, Prompt: svc.SDK.prompt(sub.ID, sub.Title), SessionUUID: svc.SDK.NewUUID(),
			}
			b, err := svc.SDK.Reserve(m)
			if err != nil {
				return "", err
			}
			begin = b
			return m.ID, nil
		})
		var ref *launch.Refusal
		switch {
		case errors.As(err, &ref):
			resp.Error(w, r, ref.Status, ref.Code, ref.Msg, ref.Details)
		case err != nil:
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		default:
			if begin != nil {
				go begin()
			}
			resp.JSON(w, r, http.StatusAccepted, pigtail.SDKLaunched{ID: id, State: pigtail.SDKStarting})
		}
	}
}

// ListSDKSessions handles GET /sdk-sessions[?subject=]: the sessions of the
// subjects the caller may read.
func ListSDKSessions(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		subject := r.URL.Query().Get("subject")
		if subject != "" {
			if _, ok := authorizeSubject(w, r, svc, resp, subject, false); !ok {
				return
			}
		}
		all, err := svc.SDK.Registry.List(subject)
		if err != nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable, err.Error(), nil)
			return
		}
		c := callerOf(r)
		seen := map[string]bool{}
		out := pigtail.SDKSessionList{Sessions: []pigtail.SDKSession{}, LaunchEnabled: svc.SDK.LaunchEnabled}
		for _, s := range all {
			vis, known := seen[s.SubjectID]
			if !known {
				vis, err = sdkVisible(svc, c, s.SubjectID, false)
				if err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
				seen[s.SubjectID] = vis
			}
			if vis {
				out.Sessions = append(out.Sessions, toWireSDK(s))
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// toWireSDK converts a session to its wire type (same JSON; the decode in
// TestSDKWireTypesMatch keeps them in step).
func toWireSDK(s sdksession.Session) pigtail.SDKSession {
	b, _ := json.Marshal(s)
	var w pigtail.SDKSession
	_ = json.Unmarshal(b, &w)
	if w.ClaudePIDs == nil {
		w.ClaudePIDs = []int{}
	}
	if w.Pending == nil {
		w.Pending = []pigtail.SDKRequest{}
	}
	return w
}

// GetSDKSession handles GET /sdk-sessions/{id}.
func GetSDKSession(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		s, ok := sdkSession(w, r, svc, resp, false)
		if !ok {
			return
		}
		resp.JSON(w, r, http.StatusOK, toWireSDK(s))
	}
}

// GetSDKEvents handles GET /sdk-sessions/{id}/events?after=N.
func GetSDKEvents(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		after := 0
		if v := r.URL.Query().Get("after"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "after must be a non-negative integer", nil)
				return
			}
			after = n
		}
		s, ok := sdkSession(w, r, svc, resp, false)
		if !ok {
			return
		}
		evs, err := svc.SDK.Registry.Events(s.ID, after)
		if err != nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable, err.Error(), nil)
			return
		}
		out := pigtail.SDKEvents{Events: evs, LastSeq: after}
		if out.Events == nil {
			out.Events = []json.RawMessage{}
		}
		for _, e := range evs {
			var h struct {
				Seq int `json:"seq"`
			}
			if json.Unmarshal(e, &h) == nil && h.Seq > out.LastSeq {
				out.LastSeq = h.Seq
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// sdkCookieOnly refuses a write that is not the web front's: an agent's
// session header or a bearer caller. true means the response is written.
func sdkCookieOnly(w http.ResponseWriter, r *http.Request, resp Responder) bool {
	if r.Header.Get(pigtail.HeaderSession) != "" || !callerOf(r).web {
		resp.Error(w, r, http.StatusForbidden, pigtail.ErrUnauthorized, "answer a SDK session from the web front, not from an agent or the CLI", nil)
		return true
	}
	return false
}

// hostCommand sends cmd and maps the outcomes: an unreachable host or a host
// refusal is a 409 (the session moved on). true means the response is written.
func hostCommand(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, id string, cmd map[string]any) bool {
	rep, err := svc.SDK.Registry.Dial(id, cmd)
	if err != nil {
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "the session's host is not reachable: "+err.Error(), nil)
		return true
	}
	if !rep.OK {
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "the host refused: "+rep.Error, nil)
		return true
	}
	return false
}

// AnswerSDKRequest handles POST /sdk-sessions/{id}/requests/{rid}/answer.
func AnswerSDKRequest(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		if sdkCookieOnly(w, r, resp) {
			return
		}
		s, ok := sdkSession(w, r, svc, resp, true)
		if !ok {
			return
		}
		var req pigtail.SDKAnswerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.Behavior != "allow" && req.Behavior != "deny" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "behavior must be allow or deny", nil)
			return
		}
		answers := map[string]any{}
		for q, raw := range req.Answers {
			var one string
			var many []string
			switch {
			case json.Unmarshal(raw, &one) == nil:
				answers[q] = one
			case json.Unmarshal(raw, &many) == nil:
				answers[q] = many
			default:
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "answers["+q+"] must be a string or a list of strings", nil)
				return
			}
		}
		rid := chi.URLParam(r, "rid")
		pending := false
		for _, p := range s.Pending {
			if p.Rid == rid {
				pending = true
			}
		}
		if !pending {
			if sdkWasAnswered(svc, s.ID, rid) {
				resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "request "+rid+" is already answered", nil)
			} else {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such request", nil)
			}
			return
		}
		cmd := map[string]any{"op": "answer", "rid": rid, "behavior": req.Behavior}
		if len(answers) > 0 {
			cmd["answers"] = answers
		}
		if req.Message != "" {
			cmd["message"] = req.Message
		}
		if hostCommand(w, r, svc, resp, s.ID, cmd) {
			return
		}
		// The question is answered: the auto wait of the session goes with it,
		// the declared ones stay.
		if s.ClaudeSessionID != "" && svc.Store != nil {
			if _, err := svc.Store.ClearSessionWaits(s.ClaudeSessionID, pigtail.WaitSourceAuto); err != nil && svc.Logf != nil {
				svc.Logf("sdk answer: clear the auto waits of %s: %v", s.ID, err)
			}
		}
		resp.JSON(w, r, http.StatusOK, map[string]any{"answered": rid})
	}
}

// sdkWasAnswered scans the journal for a request_answered of rid.
func sdkWasAnswered(svc *Services, id, rid string) bool {
	evs, err := svc.SDK.Registry.Events(id, 0)
	if err != nil {
		return false
	}
	for _, e := range evs {
		var h struct {
			Kind string `json:"kind"`
			Data struct {
				Rid string `json:"rid"`
			} `json:"data"`
		}
		if json.Unmarshal(e, &h) == nil && h.Kind == "request_answered" && h.Data.Rid == rid {
			return true
		}
	}
	return false
}

// MessageSDKSession handles POST /sdk-sessions/{id}/messages: a reply, only
// once the turn is over.
func MessageSDKSession(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		if sdkCookieOnly(w, r, resp) {
			return
		}
		s, ok := sdkSession(w, r, svc, resp, true)
		if !ok {
			return
		}
		var req pigtail.SDKMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "text is empty", nil)
			return
		}
		if s.State != pigtail.SDKIdle {
			resp.Error(w, r, http.StatusConflict, pigtail.ErrNotIdle, "the session is "+s.State+", not idle", nil)
			return
		}
		if hostCommand(w, r, svc, resp, s.ID, map[string]any{"op": "say", "text": req.Text}) {
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]any{"sent": true})
	}
}

// EndSDKSession handles POST /sdk-sessions/{id}/end.
func EndSDKSession(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.SDK == nil {
			sdkOff(w, r, svc, resp)
			return
		}
		if sdkCookieOnly(w, r, resp) {
			return
		}
		s, ok := sdkSession(w, r, svc, resp, true)
		if !ok {
			return
		}
		switch s.State {
		case pigtail.SDKEnded, pigtail.SDKFailed, pigtail.SDKLost:
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "the session is already "+s.State, nil)
			return
		}
		if hostCommand(w, r, svc, resp, s.ID, map[string]any{"op": "end"}) {
			return
		}
		resp.JSON(w, r, http.StatusOK, map[string]any{"ending": true})
	}
}

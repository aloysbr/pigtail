package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/wspath"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// PostPeers handles POST /peers: upsert (or, with end=true, mark ended) the
// listed sessions. Each row is enriched with the pigtail worktree containing
// its cwd and the subject running there, so a session is addressable by
// subject slug or worktree name without the CLI knowing about subjects. The
// rows echoed back are filtered and redacted for the caller like GET /peers;
// what is stored is not.
//
// Any snapshot carries every session, perso ones included, so any caller may
// upsert any row. But a session's scope decides who may act as it, so only a
// perso caller changes a stored scope (a session moved between cells), and
// ending a session is acting as it (actingAs): from another scope, the end is
// skipped silently, as for an unknown id.
func (rl *Relay) PostPeers(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.PeersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		// A session id reserved for a sender that is no session (the relay,
		// the user) is refused for the whole request, before anything is
		// written: a peer registered as "human" would read as the user in
		// relay.Frame (sub_x000000000000026, Task 7).
		for _, p := range req.Peers {
			if pigtail.NonPeerSender(p.SessionID) {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "session id "+p.SessionID+" is reserved", nil)
				return
			}
		}
		caller := callerOf(r).scope
		now := rl.cfg.Now()
		var workspaces []pigtail.Workspace
		if !req.End {
			workspaces, _ = rl.svc.Store.ListWorkspaces("")
		}
		out := make([]pigtail.PeerSession, 0, len(req.Peers))
		for _, p := range req.Peers {
			if p.SessionID == "" {
				continue
			}
			if req.End {
				ended, ok := rl.actingAs(caller, p.SessionID)
				if !ok {
					continue
				}
				if err := rl.endPeer(*ended, now); err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
					return
				}
				continue
			}
			prior := rl.priorOf(p.SessionID)
			if !scope.SeesAll(caller) {
				if old := prior.row; old != nil && old.Scope != "" {
					// Empty fields never erase stored ones (UpsertPeer): the
					// session keeps the cell and scope it was registered with.
					p.Scope, p.CellID, p.ConfigDir = "", "", ""
				}
			}
			alive := rl.provenAlive(&p, prior)
			rl.enrich(&p, workspaces, alive, prior)
			p.UpdatedAt = now
			if err := rl.svc.Store.UpsertPeer(&p); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			rl.restoreEndedWaits(p.SessionID, p.Source)
			if got, err := rl.svc.Store.GetPeer(p.SessionID); err == nil && got != nil {
				q := *got
				q.Source = p.Source
				rl.resumePause(q)
			}
			if p.Source == "hook" && p.CmuxSurfaceID != "" {
				rl.endStandbyReplaced(p)
				rl.endPauseReplaced(p)
			}
			if got, err := rl.svc.Store.GetPeer(p.SessionID); err == nil {
				if got.SubjectID != "" {
					// D29-C: an answer kept for its subject goes to it now —
					// no wake, no cmux call in the hook's request (C4).
					rl.deliverKeptAnswers(got.SessionID, false)
				}
				out = append(out, *got)
			}
		}
		out = visiblePeers(caller, out)
		rl.redactPeers(caller, out)
		for i := range out {
			out[i] = rl.decorate(out[i])
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// endPeer marks the session ended, as its SessionEnd does: the waits it
// emitted close, its subject's stay (D29-A); an answer it was handed and
// never read is kept again (C3). Shared by POST /peers end=true and the idle
// pause (relay_pause.go), which ends the peer before its SIGTERM.
func (rl *Relay) endPeer(p pigtail.PeerSession, now time.Time) error {
	// The automatic handoff reads the session's waits, which the end closes:
	// promote first. Its failure never fails the end (the draft is kept and
	// the sweep retries).
	if err := rl.promoteHandoffDraft(p, promoteEnd, now); err != nil {
		rl.svc.logf("handoff-auto: %s: promote at end: %v", p.SessionID, err)
	}
	closed, reheld, err := rl.svc.Store.EndPeerClosing(p.SessionID, now)
	if err != nil {
		return err
	}
	rl.refreshWaitMarker(p.SessionID)    // EndPeerClosing dropped its waits
	rl.refreshStandbyMarker(p.SessionID) // kept: the row outlives the end (D24)
	if len(reheld) > 0 {
		rl.refreshMarker(p.SessionID) // its expired answers left the inbox
	}
	rl.publishWaitsCleared(p, closed)
	return nil
}

// endStandbyReplaced ends the Standby of the sessions whose surface p, by
// its own SessionStart (Source hook, never a snapshot: its tty attribution can
// be stale), now holds under another id — /clear, a fresh claude, a respawn
// (D24). A relaunch resumes the same id and keeps it. A store error is logged;
// the registration answers as before.
func (rl *Relay) endStandbyReplaced(p pigtail.PeerSession) {
	ids, err := rl.svc.Store.EndStandbyReplacedOnSurface(p.CmuxSurfaceID, p.SessionID, rl.cfg.Now())
	if err != nil {
		rl.svc.logf("relay: end standby replaced on %s: %v", p.CmuxSurfaceID, err)
		return
	}
	for _, id := range ids {
		rl.refreshStandbyMarker(id)
		old := pigtail.PeerSession{SessionID: id}
		if got, err := rl.svc.Store.GetPeer(id); err == nil {
			old = *got
		}
		rl.publishStandby(old, false)
		rl.svc.logf("relay: %s standby ended: %s took its surface", id, p.SessionID)
	}
}

// GetPeers handles GET /peers[?all=1]: the live sessions, each with its label,
// liveness and pending count. all=1 includes ended and dead sessions. Only the
// sessions the caller may see are listed, and the subject of one working on a
// subject the caller may not read is stripped (see relay_scope.go).
func (rl *Relay) GetPeers(resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all := r.URL.Query().Get("all") != ""
		ps, err := rl.svc.Store.ListPeers(all)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		caller := callerOf(r).scope
		ps = visiblePeers(caller, ps)
		rl.redactPeers(caller, ps)
		out := make([]pigtail.PeerSession, 0, len(ps))
		for _, p := range ps {
			// Dead rows are dropped before decorate (a sidebar read, three
			// SQLite reads each), unless every row is asked for.
			if !all && !rl.cfg.Alive(p) {
				continue
			}
			out = append(out, rl.decorate(p))
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// livePeers lists the peers a message can be addressed to.
func (rl *Relay) livePeers() ([]pigtail.PeerSession, error) {
	ps, err := rl.svc.Store.ListPeers(false)
	if err != nil {
		return nil, err
	}
	out := ps[:0]
	for _, p := range ps {
		if rl.cfg.Alive(p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (rl *Relay) decorate(p pigtail.PeerSession) pigtail.PeerSession {
	p.Label = rl.labelOf(&p)
	alive := rl.cfg.Alive(p)
	p.Alive = &alive
	p.Inbox, _ = rl.svc.Store.CountPending(p.SessionID, rl.cfg.Now())
	p.Waits, _ = rl.svc.Store.SessionWaits(p.SessionID)
	if row, err := rl.svc.Store.GetPause(p.SessionID); err == nil && row != nil && row.State == pigtail.PausePaused {
		p.PausedAt = row.PausedAt
	}
	return p
}

// applyLiveTitles puts, in place, the sidebar loop's live title on every peer
// it shows: it wins over the CLI snapshot's (dated by the last `msg`/`session
// ls`, and blind to a hand-given title's leading symbol). Call it after
// redactPeers and before resolving an address or labelling. A redacted peer
// never gets a title back, and a workspace the loop binds to another subject
// than the peer's keeps the store's.
func (rl *Relay) applyLiveTitles(ps []pigtail.PeerSession) {
	if rl.svc.CmuxSidebar == nil {
		return
	}
	ws := rl.svc.CmuxSidebar.State().Workspaces
	for i := range ps {
		p := &ps[i]
		if p.CmuxWorkspaceID == "" || p.TitleRedacted {
			continue
		}
		if t, ok := liveTitle(ws, p); ok {
			p.CmuxTitle = t
		}
	}
}

func liveTitle(ws []pigtail.CmuxSidebarWorkspace, p *pigtail.PeerSession) (string, bool) {
	for _, w := range ws {
		if !strings.EqualFold(w.CmuxWorkspaceID, p.CmuxWorkspaceID) || w.Title == "" {
			continue
		}
		if w.TitleOwner != "" && (w.Subject == nil || w.Subject.ID != p.SubjectID) {
			return "", false // bound: only when the loop and the peer agree on the subject
		}
		return w.Title, true
	}
	return "", false
}

// labelOf is the one place the daemon labels a peer, from its live title.
func (rl *Relay) labelOf(p *pigtail.PeerSession) string {
	one := []pigtail.PeerSession{*p}
	rl.applyLiveTitles(one)
	*p = one[0] // GET /peers serves the live title as cmux_title
	return peers.Label(*p)
}

// peerPrior is a session's stored row as read once per upsert: row is nil for
// an unknown id, fault says the read failed (the stored row is then unknown).
type peerPrior struct {
	row   *pigtail.PeerSession
	fault bool
}

func (rl *Relay) priorOf(sessionID string) peerPrior {
	row, err := rl.svc.Store.GetPeer(sessionID)
	switch {
	case err == nil:
		return peerPrior{row: row}
	case errors.Is(err, store.ErrNotFound):
		return peerPrior{}
	default:
		return peerPrior{fault: true}
	}
}

// provenAlive says an upsert is a sign of life: the session's own hook, or a
// row with a pid that still runs and names the session (a snapshot included).
// The config dir falls back to the stored row's: PostPeers blanks it for a
// non-master caller and cmux may omit session_dir. Call it with p.Source set.
func (rl *Relay) provenAlive(p *pigtail.PeerSession, prior peerPrior) bool {
	if p.Source == "hook" {
		return true
	}
	if p.PID <= 0 {
		return false
	}
	cfg := p.ConfigDir
	if cfg == "" && prior.row != nil {
		cfg = prior.row.ConfigDir
	}
	return rl.sessionAlive(cfg, p.PID, p.SessionID)
}

// enrich fills the worktree and subject of a peer from its cwd: the deepest
// pigtail workspace whose path contains the cwd, and the subject whose phase
// runs there.
//
// Subject (sub_x000000000000021): a session proven alive is stamped with the
// subject its cmux workspace is bound to, else with the worktree deduction,
// written as is (empty included, SubjectSet). A session not proven alive never
// changes a non-empty stored subject; the incoming value and the deduction
// fill only an empty one. A failed lookup takes the not-alive path.
func (rl *Relay) enrich(p *pigtail.PeerSession, workspaces []pigtail.Workspace, alive bool, prior peerPrior) {
	if p.WorkspaceID == "" && p.Cwd != "" {
		if bestWS, ok := wspath.Deepest(p.Cwd, workspaces); ok {
			p.WorkspaceID = bestWS.ID
			p.WorkspaceName = filepath.Base(bestWS.Path)
		}
	}
	if alive && !prior.fault && rl.stampAlive(p, prior) {
		return
	}
	switch {
	case prior.fault:
		p.SubjectID = "" // unknown stored value: never risk overwriting it
		return
	case prior.row != nil && prior.row.SubjectStamp != "":
		p.SubjectID = "" // the merge keeps the stored one
		return
	}
	if p.SubjectID == "" && p.WorkspaceID != "" {
		if id, err := rl.svc.Store.SubjectForWorkspace(p.WorkspaceID); err == nil {
			p.SubjectID = id
		}
	}
}

// stampAlive sets the subject of a proven-alive session; false on a fault.
func (rl *Relay) stampAlive(p *pigtail.PeerSession, prior peerPrior) bool {
	ws := p.CmuxWorkspaceID
	if ws == "" && prior.row != nil {
		ws = prior.row.CmuxWorkspaceID
	}
	subject := ""
	bound := false
	if strings.TrimSpace(ws) != "" {
		id, err := pigtail.NormalizeCmuxWorkspaceID(ws)
		if err != nil {
			return false
		}
		b, err := rl.svc.Store.GetCmuxBinding(id)
		switch {
		case err == nil:
			subject, bound = b.SubjectID, true
		case !errors.Is(err, store.ErrNotFound):
			return false
		}
	}
	if !bound && p.WorkspaceID != "" {
		id, err := rl.svc.Store.SubjectForWorkspace(p.WorkspaceID)
		switch {
		case err == nil:
			subject = id
		case !errors.Is(err, store.ErrNotFound):
			return false
		}
	}
	p.SubjectID, p.SubjectSet = subject, true
	return true
}

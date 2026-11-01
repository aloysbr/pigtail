package cmuxsync

import (
	"context"
	"fmt"
	"strings"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ToggleResult is what a toggle did: the state the folders were left in and
// how many it moved (pigtail.CmuxFoldersToggle, the wire answer).
type ToggleResult = pigtail.CmuxFoldersToggle

// ErrFoldersIdle is the refusal of a toggle: nothing was written.
var ErrFoldersIdle = pigtail.ErrCmuxFoldersIdle

type toggleReply struct {
	res ToggleResult
	err error
}

type toggleReq struct {
	ctx   context.Context
	reply chan toggleReply // buffer 1: the server never waits on a caller
}

// ToggleFolders is the D12 key (sub_x000000000000041): the user's gesture, never
// the arrangement's. Only pigtail's own folders count and move: one expanded
// (Standby included) → collapse every expanded one; none → expand them all
// but Standby, which stays collapsed. anchorHosts is ArrangeInput.AnchorHosts:
// a group whose anchor hosts a session is not an own folder (splitGroups).
func ToggleFolders(folders []string, groups []cmux.Group, anchorHosts map[string]bool) []FolderOp {
	own, _ := splitGroups(ArrangeInput{Folders: folders, Groups: groups, AnchorHosts: anchorHosts})
	var open []FolderOp
	for _, f := range folders {
		if gr := own[f]; gr != nil && !gr.Collapsed {
			open = append(open, FolderOp{Kind: FolderCollapse, Group: gr.ID})
		}
	}
	if len(open) > 0 {
		return open
	}
	var ops []FolderOp
	for _, f := range folders {
		if gr := own[f]; gr != nil && f != space.Standby {
			ops = append(ops, FolderOp{Kind: FolderExpand, Group: gr.ID})
		}
	}
	return ops
}

// ToggleFolders folds or unfolds the folders on the user's request (D12), in
// every allowed cmux window, each by its own state (D8 of
// sub_x000000000000007). The
// write runs on the Run goroutine, between two passes (serveToggle): it never
// interleaves with the arrangement's group writes, and collapsedMem has one
// writer. It applies even while cmux is key (D2 aims at the arrangement).
func (r *Runner) ToggleFolders(ctx context.Context) (ToggleResult, error) {
	reply, err := r.enqueueToggle(ctx)
	if err != nil {
		return ToggleResult{}, err // ErrFoldersIdle or ctx.Err(), nothing queued
	}
	select {
	case rep := <-reply:
		return rep.res, rep.err
	case <-r.done:
		select { // served just before Run returned?
		case rep := <-reply:
			return rep.res, rep.err
		default:
			return ToggleResult{}, ErrFoldersIdle
		}
	case <-ctx.Done():
		return ToggleResult{}, ctx.Err() // serveToggle drops a request whose ctx is done
	}
}

// enqueueToggle: the cheap refusals first (loop off, folders off), then a send
// on toggleCh bounded by ctx and r.done, then a wake-up.
func (r *Runner) enqueueToggle(ctx context.Context) (<-chan toggleReply, error) {
	r.mu.Lock()
	on := r.want && !r.panicked
	r.mu.Unlock()
	if !on || !r.cfg.Folders || r.deps.Folders == nil {
		return nil, ErrFoldersIdle
	}
	select {
	case <-r.done:
		return nil, ErrFoldersIdle
	default:
	}
	q := toggleReq{ctx: ctx, reply: make(chan toggleReply, 1)}
	select {
	case r.toggleCh <- q:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.done:
		return nil, ErrFoldersIdle
	}
	r.poke()
	return q.reply, nil
}

// answerQueuedToggle refuses a request still queued when Run stops.
func (r *Runner) answerQueuedToggle() {
	select {
	case q := <-r.toggleCh:
		q.reply <- toggleReply{err: ErrFoldersIdle}
	default:
	}
}

// serveToggle runs on the Run goroutine (head of Step, before pass). It always
// answers, also when it panics before it did (the recover of Step logs it),
// and drops a request whose caller is gone. It re-reads every condition
// itself: Run is the only goroutine that changes them while it serves.
func (r *Runner) serveToggle(q toggleReq) {
	answered := false
	defer func() {
		if !answered {
			q.reply <- toggleReply{err: ErrFoldersIdle}
		}
	}()
	answer := func(res ToggleResult, err error) {
		answered = true
		q.reply <- toggleReply{res: res, err: err}
	}
	if q.ctx.Err() != nil { // the caller left: no write
		answered = true
		return
	}
	r.mu.Lock()
	on := r.want && !r.panicked
	down := r.listDown
	wins, at := r.foldWins, r.foldWinsAt
	r.mu.Unlock()
	switch {
	case !on, !r.cfg.Folders, r.deps.Folders == nil, r.deps.FolderOrder == nil, r.foldersStopped, down,
		at.IsZero(), r.deps.Now().Sub(at) > 2*r.cfg.SlowInterval, len(wins) == 0:
		answer(ToggleResult{}, ErrFoldersIdle)
		return
	}
	r.loadCollapse()
	// The open focus are folders too (sub_x000000000000007); a read error
	// answers, never reads as « no focus »: nothing is written.
	fv, err := r.readFocus()
	if err != nil {
		answer(ToggleResult{}, fmt.Errorf("focus: %w", err))
		return
	}
	hosted, err := r.hostedSet()
	if err != nil {
		answer(ToggleResult{}, err)
		return
	}
	// D8, focus D12: every allowed window, each by its own state. Folders is
	// the sum; Collapsed is the first window that wrote.
	var res ToggleResult
	decided := false
	planned := 0
	var applyErr error
	for _, win := range wins {
		groups, err := r.deps.Folders.ListGroups(win)
		if err != nil {
			applyErr = err
			break
		}
		anchorHosts := map[string]bool{}
		for _, gr := range groups {
			if gr.AnchorGenerated && hosted[strings.ToUpper(gr.AnchorID)] {
				anchorHosts[gr.AnchorID] = true
			}
		}
		ops := ToggleFolders(r.folderOrder(fv), groups, anchorHosts)
		planned += len(ops)
		if len(ops) > 0 && !decided {
			res.Collapsed, decided = ops[0].Kind == FolderCollapse, true
		}
		name := map[string]string{}
		for _, gr := range groups {
			name[gr.ID] = gr.Name
		}
		for _, op := range ops {
			if res.Folders > 0 {
				r.deps.Sleep(r.cfg.OpInterval)
			}
			if applyErr = r.applyFolderOp(win, op); applyErr != nil {
				break
			}
			r.collapsedMem[name[op.Group]] = op.Kind == FolderCollapse
			res.Folders++
		}
		if applyErr != nil {
			break
		}
	}
	if res.Folders > 0 && r.deps.SaveCollapse != nil {
		if err := r.deps.SaveCollapse(r.collapsedMem); err != nil {
			r.deps.Logf("cmuxsync: folders: save folded state: %v", err)
		}
	}
	if applyErr != nil {
		r.countError(r.deps.Now())
		r.deps.Logf("cmuxsync: folders: toggle stopped after %d of %d folders: %v", res.Folders, planned, applyErr)
		answer(ToggleResult{}, applyErr)
		return
	}
	state := "expanded"
	if res.Collapsed {
		state = "collapsed"
	}
	r.deps.Logf("cmuxsync: folders: %d folders %s (toggle)", res.Folders, state)
	answer(res, nil)
}

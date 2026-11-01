package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/handoffdraft"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// handoffDraftDir must agree with the daemon's RelayConfig.DraftDir: the
// directory of the socket, plus "handoff-draft" (same rule as waitMarkerDir, so
// a test or live-pass daemon on its own socket never sees the real drafts).
func handoffDraftDir(getenv func(string) string) string {
	if d := getenv("PIGTAIL_HANDOFF_DRAFT_DIR"); d != "" {
		return d
	}
	if s := socketPath(); s != "" {
		return filepath.Join(filepath.Dir(s), "handoff-draft")
	}
	return filepath.Join(setup.ConfigDir(), "handoff-draft")
}

// recordHandoffDraft writes the session's draft handoff at a Stop: a small
// file, no network, no git, no model. It never blocks and never fails the
// hook: every error is one shape-only line in the hook's local log.
//
// PIGTAIL_HANDOFF_DRAFT=off is a TEST switch: with it set in production the
// daemon sees no draft for any bound session and journals a "draft missing"
// (handoff-auto-failed) row per session.
func recordHandoffDraft(getenv func(string) string, in waittype.HookInput, turn claudesession.Turn) {
	defer func() { _ = recover() }()
	if relayOff(getenv, "PIGTAIL_HANDOFF_DRAFT") || turn.ID == "" {
		return
	}
	log := hookLogger{hook: "handoff-draft", getenv: getenv, now: time.Now, sid: in.SessionID, cwd: in.Cwd}
	dir := handoffDraftDir(getenv)
	var turnAt time.Time
	if prev, err := handoffdraft.Read(dir, in.SessionID); err == nil && prev.TurnID == turn.ID && !prev.TurnAt.IsZero() {
		turnAt = prev.TurnAt
	} else if at, ok := claudesession.TurnStartOf(in.TranscriptPath, turn.ID); ok {
		turnAt = at
	} else {
		turnAt = turn.Start
		log.write("draft-turn-at", "turn_id="+turn.ID+" whole-transcript scan impossible: using ReadLastTurn start")
	}
	tldr, tail := handoffdraft.ExtractTLDR(in.LastAssistantMessage)
	d := handoffdraft.Draft{
		SessionID: in.SessionID, TurnID: turn.ID, TurnAt: turnAt,
		Cwd: in.Cwd, Branch: readBranch(in.Cwd), TLDR: tldr, Tail: tail,
	}
	if err := handoffdraft.Write(dir, d); err != nil {
		log.write("draft-error", "turn_id="+turn.ID+" err="+err.Error())
		return
	}
}

// readBranch returns the branch checked out at cwd, read from .git/HEAD (or the
// gitdir a worktree's .git file points to); "" when unknown or detached. It
// climbs at most 6 directories and never runs git.
func readBranch(cwd string) string {
	dir := cwd
	for i := 0; i < 6 && dir != "" && dir != string(filepath.Separator) && dir != "."; i++ {
		g := filepath.Join(dir, ".git")
		fi, err := os.Stat(g)
		if err == nil {
			head := filepath.Join(g, "HEAD")
			if !fi.IsDir() {
				b, err := os.ReadFile(g)
				if err != nil {
					return ""
				}
				gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
				if !ok {
					return ""
				}
				gd = strings.TrimSpace(gd)
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(dir, gd)
				}
				head = filepath.Join(gd, "HEAD")
			}
			b, err := os.ReadFile(head)
			if err != nil {
				return ""
			}
			ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
			if !ok {
				return ""
			}
			return ref
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

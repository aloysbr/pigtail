package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var colT0 = time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)

type colRig struct {
	t      *testing.T
	cfg    string
	peers  []pigtail.PeerSession
	alive  map[string]bool
	native map[string]claudesession.Native // by session id
	noNat  map[string]bool
	files  map[string]string // session id -> transcript path ("" = none)
	ambig  map[string]bool
	c      *collector
}

func newColRig(t *testing.T) *colRig {
	r := &colRig{t: t, cfg: t.TempDir(), alive: map[string]bool{}, native: map[string]claudesession.Native{},
		noNat: map[string]bool{}, files: map[string]string{}, ambig: map[string]bool{}}
	r.c = &collector{
		loops: claudesession.NewLoopReader(),
		peers: func(subject string, ended bool, limit int) ([]pigtail.PeerSession, error) { return r.peers, nil },
		readNative: func(cfg string, pid int) (claudesession.Native, bool) {
			for _, p := range r.peers {
				if p.PID == pid {
					if r.noNat[p.SessionID] {
						return claudesession.Native{}, false
					}
					return r.native[p.SessionID], true
				}
			}
			return claudesession.Native{}, false
		},
		alive: func(cfg string, pid int, sid string) bool { return r.alive[sid] },
		find: func(sid string, cfgs []string) (claudesession.Transcript, bool, error) {
			if r.ambig[sid] {
				return claudesession.Transcript{}, false, fmt.Errorf("ambiguous %s", sid)
			}
			p := r.files[sid]
			if p == "" {
				return claudesession.Transcript{}, false, nil
			}
			return claudesession.Transcript{ID: sid, ConfigDir: filepath.Dir(filepath.Dir(filepath.Dir(p))), Slug: filepath.Base(filepath.Dir(p))}, true, nil
		},
	}
	return r
}

func (r *colRig) session(sid string, pid int, live bool, status string, lines ...string) {
	p := pigtail.PeerSession{SessionID: sid, ConfigDir: r.cfg, PID: pid, Scope: "perso"}
	if !live {
		e := colT0
		p.EndedAt = &e
	}
	r.peers = append(r.peers, p)
	r.alive[sid] = live
	r.native[sid] = claudesession.Native{Status: status, StatusUpdatedAt: colT0.Add(time.Minute).UnixMilli()}
	if lines != nil {
		dir := filepath.Join(r.cfg, "projects", "slug")
		_ = os.MkdirAll(dir, 0o700)
		path := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			r.t.Fatal(err)
		}
		r.files[sid] = path
	}
}

func colArm(at, due time.Duration) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"%s","toolUseResult":{"scheduledFor":%d}}`,
		colT0.Add(at).Format("2006-01-02T15:04:05.000Z"), colT0.Add(due).UnixMilli())
}
func colTurn(at time.Duration) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s"}`, colT0.Add(at).Format("2006-01-02T15:04:05.000Z"))
}

var colRole = pigtail.Role{Scope: "perso", Name: "pilote", SubjectID: "sub", Cadence: 30 * time.Minute}

func TestCollectFollowsSubjectAcrossSessionIDs(t *testing.T) {
	r := newColRig(t)
	r.session("old", 1, false, "idle", colArm(0, 30*time.Minute), colTurn(time.Minute))
	r.session("new", 2, true, "idle", colTurn(40*time.Minute))
	o := r.c.collect(colRole)
	if o.Err != nil || len(o.Live) != 1 || o.Live[0].SessionID != "new" {
		t.Fatalf("%+v", o)
	}
	if !o.Loop.ScheduledFor.Equal(colT0.Add(30*time.Minute)) || !o.Loop.TurnAt.Equal(colT0.Add(40*time.Minute)) {
		t.Fatalf("loop = %+v", o.Loop)
	}
}

func TestCollectSubjectFromCmuxBinding(t *testing.T) {
	r := newColRig(t)
	r.session("bound", 3, true, "busy", colTurn(time.Minute))
	r.peers[0].SubjectID = "sub" // the store resolves it through the binding; the seam hands it back
	o := r.c.collect(colRole)
	if o.Err != nil || len(o.Live) != 1 || o.Live[0].Native != "busy" {
		t.Fatalf("%+v", o)
	}
}

func TestCollectIgnoresRoleScope(t *testing.T) {
	r := newColRig(t)
	r.session("other", 4, true, "idle", colTurn(time.Minute))
	r.peers[0].Scope = "globex"
	if o := r.c.collect(colRole); o.Err != nil || len(o.Live) != 1 {
		t.Fatalf("%+v", o)
	}
}

func TestCollectEndedWithoutTranscriptSkipped(t *testing.T) {
	r := newColRig(t)
	r.session("ended", 5, false, "idle") // no transcript
	r.session("live", 6, true, "idle", colTurn(time.Minute))
	o := r.c.collect(colRole)
	if o.Err != nil || len(o.Live) != 1 || o.Live[0].SessionID != "live" {
		t.Fatalf("%+v", o)
	}
	r2 := newColRig(t)
	r2.session("fresh", 7, true, "idle") // live, nothing written yet
	o = r2.c.collect(colRole)
	if o.Err != nil || len(o.Live) != 1 || !o.Live[0].ActivityMtime.Equal(o.Live[0].StatusSince) {
		t.Fatalf("%+v", o)
	}
}

func TestCollectLiveNativeUnreadableIsErr(t *testing.T) {
	r := newColRig(t)
	r.session("live", 8, true, "idle", colTurn(time.Minute))
	r.noNat["live"] = true
	o := r.c.collect(colRole)
	if o.Err == nil || !strings.Contains(o.Err.Error(), "live") || len(o.Live) != 0 {
		t.Fatalf("%+v", o)
	}
}

func TestCollectFindErrorIsErr(t *testing.T) {
	r := newColRig(t)
	r.session("amb", 9, true, "idle", colTurn(time.Minute))
	r.ambig["amb"] = true
	if o := r.c.collect(colRole); o.Err == nil {
		t.Fatalf("%+v", o)
	}
}

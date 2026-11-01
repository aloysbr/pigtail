package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var roleT0 = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func TestRoleUpsertGetListDelete(t *testing.T) {
	st := openMem(t)
	r := &pigtail.Role{Scope: "perso", Name: "pilote", SubjectID: "sub_a", Cadence: 30 * time.Minute,
		Grace: 10 * time.Minute, Stall: 20 * time.Minute, CreatedAt: roleT0, UpdatedAt: roleT0}
	if err := st.UpsertRole(r); err != nil {
		t.Fatal(err)
	}
	r2 := *r
	r2.Cadence = time.Hour
	r2.CreatedAt = roleT0.Add(time.Hour)
	r2.UpdatedAt = roleT0.Add(time.Hour)
	if err := st.UpsertRole(&r2); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRole(scope.Scope("perso"), "pilote")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cadence != time.Hour || !got.CreatedAt.Equal(roleT0) || !got.UpdatedAt.Equal(roleT0.Add(time.Hour)) {
		t.Fatalf("after second upsert = %+v", got)
	}
	other := &pigtail.Role{Scope: "work-x", Name: "other", SubjectID: "sub_b", Cadence: time.Minute, Grace: time.Minute, Stall: time.Minute, CreatedAt: roleT0, UpdatedAt: roleT0}
	if err := st.UpsertRole(other); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListRoles(scope.Any)
	if err != nil || len(all) != 2 {
		t.Fatalf("list any = %v, %v", all, err)
	}
	one, err := st.ListRoles(scope.Scope("perso"))
	if err != nil || len(one) != 1 || one[0].Name != "pilote" {
		t.Fatalf("list perso = %v, %v", one, err)
	}
	if err := st.DeleteRole(scope.Scope("perso"), "pilote"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRole(scope.Scope("perso"), "pilote"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	if err := st.DeleteRole(scope.Scope("perso"), "pilote"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestPeersOfSubjectByBindingNotScope(t *testing.T) {
	st := openMem(t)
	mk := func(id, sc, subj, ws string, ended bool, at time.Time) {
		p := &pigtail.PeerSession{SessionID: id, Scope: sc, SubjectID: subj, CmuxWorkspaceID: ws, UpdatedAt: at, SubjectSet: true}
		if err := st.UpsertPeer(p); err != nil {
			t.Fatal(err)
		}
		if ended {
			if err := st.EndPeer(id, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	bindSubject(t, st, "sub_ours", "WS-OURS")
	mk("bound-elsewhere-stamp", "perso", "sub_other", "WS-OURS", false, roleT0.Add(1*time.Minute))
	mk("other-scope", "globex", "sub_ours", "", false, roleT0.Add(2*time.Minute))
	mk("foreign", "perso", "sub_other", "", false, roleT0.Add(3*time.Minute))
	mk("ended", "perso", "sub_ours", "", true, roleT0.Add(4*time.Minute))
	live, err := st.PeersOfSubject("sub_ours", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range live {
		ids[p.SessionID] = true
	}
	if len(live) != 2 || !ids["bound-elsewhere-stamp"] || !ids["other-scope"] {
		t.Fatalf("live = %v", ids)
	}
	all, err := st.PeersOfSubject("sub_ours", true, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	if all[2].SessionID != "ended" {
		t.Fatalf("live first expected, got order %s %s %s", all[0].SessionID, all[1].SessionID, all[2].SessionID)
	}
	lim, err := st.PeersOfSubject("sub_ours", true, 1)
	if err != nil || len(lim) != 1 || lim[0].EndedAt != nil {
		t.Fatalf("limit = %v, %v", lim, err)
	}
}

func bindSubject(t *testing.T, st *Store, subject, ws string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO subject_cmux_bindings (cmux_workspace_id, subject_id, source, bound_at) VALUES (?, ?, 'attach', 100)`, ws, subject); err != nil {
		t.Fatal(err)
	}
}

// A connection held by an open transaction makes Ping return the ctx error
// within its deadline, and the held transaction still commits.
func TestStorePingBoundedWhenConnHeld(t *testing.T) {
	st := openMem(t)
	if err := st.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := st.Ping(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("ping = %v after %s", err, time.Since(start))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := st.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

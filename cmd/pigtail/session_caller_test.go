package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// TestResolveCallerSharedGuards pins the caller resolution spawn and respawn
// share: the same guards and messages as spawn's, prefixed and worded for the
// command that asked.
func TestResolveCallerSharedGuards(t *testing.T) {
	respawnErrf := func(format string, a ...any) error { return fmt.Errorf("session respawn: "+format, a...) }
	cases := []struct {
		name      string
		setup     func(t *testing.T, f *spawnFake)
		subject   string
		cmd       string
		errf      func(string, ...any) error
		wantErr   string
		wantNoErr []string // substrings the error must NOT carry
	}{
		{"no caller", func(_ *testing.T, f *spawnFake) { f.caller = claudesession.Proc{} },
			"sub_p", "spawn", spawnErr, "session spawn: no Claude session above this pigtail — spawn only runs", nil},
		{"no caller respawn wording", func(_ *testing.T, f *spawnFake) { f.caller = claudesession.Proc{} },
			"sub_p", "respawn", respawnErrf, "session respawn: no Claude session above this pigtail — respawn only runs", []string{"— spawn only runs"}},
		{"caller env unreadable", func(_ *testing.T, f *spawnFake) { f.badEnv[4242] = true },
			"sub_p", "spawn", spawnErr, "cannot read the environment of the calling Claude", nil},
		{"config dir mismatch", func(t *testing.T, _ *spawnFake) {
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "work"))
		}, "sub_t", "spawn", spawnErr, "the cell is the session's", nil},
		{"no cell", func(t *testing.T, f *spawnFake) {
			d := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", d)
			f.envs[4242] = "CMUX_WORKSPACE_ID=" + spawnCallerWS + " CLAUDE_CONFIG_DIR=" + d
		}, "sub_p", "respawn", respawnErrf, "session respawn: no Claude cell owns", []string{"— spawn only runs"}},
		{"subject other scope", nil, "sub_t", "spawn", spawnErr, "--cross-scope", nil},
		{"subject not found", nil, "sub_nope", "respawn", respawnErrf, "subject sub_nope not found", nil},
		{"no stamp", func(_ *testing.T, f *spawnFake) { f.stamp = "" },
			"sub_p", "spawn", spawnErr, "current permission mode unknown", nil},
		{"no stamp respawn names respawn", func(_ *testing.T, f *spawnFake) {
			f.stamp = ""
			spawnPermissionStamp = func(sid string) (string, error) { return readPermissionStamp(sid, time.Now()) }
			t.Setenv("PIGTAIL_PERMISSION_STAMP_DIR", t.TempDir())
		}, "sub_p", "respawn", respawnErrf, "as a plain `pigtail session respawn …` Bash call", []string{"pigtail session spawn"}},
		{"bypass", func(_ *testing.T, f *spawnFake) { f.stamp = "bypassPermissions" },
			"sub_p", "respawn", respawnErrf, "bypassPermissions now", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			s, _ := realSubjectDaemon(t)
			f := installSpawnFake(t)
			if c.setup != nil {
				c.setup(t, f)
			}
			var err error
			withStubDaemon(t, s, func() {
				cl, cerr := newScopedClient()
				if cerr != nil {
					t.Fatal(cerr)
				}
				_, err = resolveCaller(context.Background(), cl, c.cmd, c.subject, false, c.errf)
			})
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			for _, no := range c.wantNoErr {
				if strings.Contains(err.Error(), no) {
					t.Fatalf("err = %v, must not contain %q", err, no)
				}
			}
		})
	}
}

func TestResolveCallerHappyPath(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.stamp = "plan"
	var got callerSession
	var err error
	withStubDaemon(t, s, func() {
		cl, cerr := newScopedClient()
		if cerr != nil {
			t.Fatal(cerr)
		}
		got, err = resolveCaller(context.Background(), cl, "respawn", "sub_p", false, spawnErr)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Cell.ID != "p" || got.SessionID != "sess-caller" || got.Perm.Mode != "plan" || got.Proc.PID != 4242 ||
		got.Subject.ID != "sub_p" || got.Subject.Scope != "perso" || !strings.Contains(got.Env, "CMUX_WORKSPACE_ID="+spawnCallerWS) {
		t.Fatalf("caller = %+v", got)
	}
}

// TestResolveCallerOrderUnchanged pins respawn's refusal order: a missing
// subject is reported before a missing permission record (subject read comes
// before the permission steps), and the composed parts exist.
func TestResolveCallerOrderUnchanged(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.stamp = "" // would refuse at the permission step
	var err error
	withStubDaemon(t, s, func() {
		c, cerr := newScopedClient()
		if cerr != nil {
			t.Fatal(cerr)
		}
		_, err = resolveCaller(context.Background(), c, "respawn", "sub_nope", false, respawnErr)
	})
	if err == nil || !strings.Contains(err.Error(), "subject sub_nope not found") {
		t.Fatalf("order changed: %v", err)
	}
	cs, err := resolveCallerCell("spawn", spawnErr)
	if err != nil || cs.Cell.ID != "p" || cs.Cells == nil {
		t.Fatalf("resolveCallerCell = %+v %v", cs, err)
	}
}

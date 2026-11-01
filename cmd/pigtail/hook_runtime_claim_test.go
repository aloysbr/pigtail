package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/runtimeclaim"
)

func TestRuntimeClaimHookOnceOnOff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	getenv := func(k string) string { return env[k] }
	st, err := runtimeclaim.Claim(dir, runtimeclaim.State{Session: "sess-A", Reason: "acceptance #218", PR: 218, Branch: "feat/a", SHA: shaA},
		false, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hook := func(payload string) (int, string, string) {
		var out, errw bytes.Buffer
		rc := runRuntimeClaimHook(strings.NewReader(payload), &out, &errw, getenv, time.Now)
		if out.Len() == 0 {
			return rc, "", errw.String()
		}
		var v struct {
			H struct {
				Event string `json:"hookEventName"`
				Ctx   string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatalf("hook output is not JSON: %s", out.String())
		}
		return rc, v.H.Event + "\n" + v.H.Ctx, errw.String()
	}
	b := `{"session_id":"sess-B","hook_event_name":"UserPromptSubmit"}`
	rc, out, errs := hook(b)
	if rc != 0 || !strings.Contains(out, `<pigtail-runtime-claim id="`+st.ID+`" phase="on">`) || !strings.Contains(out, "acceptance #218") ||
		!strings.Contains(out, "aaaaaaaaaaaa") || !strings.Contains(out, "PR #218") || !strings.HasPrefix(out, "UserPromptSubmit\n") {
		t.Fatalf("first: rc=%d out=%s err=%s", rc, out, errs)
	}
	if _, out, _ := hook(b); out != "" {
		t.Fatalf("second must be silent: %s", out)
	}
	if _, out, _ := hook(`{"session_id":"sess-A"}`); out != "" {
		t.Fatalf("the holder got a notice: %s", out)
	}
	if _, out, _ := hook(`{"session_id":"sess-C","agent_id":"a1"}`); out != "" {
		t.Fatalf("a subagent got a notice: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, runtimeclaim.SeenDirName, "sess-C")); !os.IsNotExist(err) {
		t.Fatalf("a subagent touched the witness: %v", err)
	}
	if _, _, err := runtimeclaim.Release(dir, "sess-A", false, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := hook(b); !strings.Contains(out, `phase="off"`) || !strings.Contains(out, "released") {
		t.Fatalf("off: %s", out)
	}
	if _, out, _ := hook(b); out != "" {
		t.Fatalf("off twice: %s", out)
	}
	if _, out, _ := hook(`{"session_id":"sess-A"}`); out != "" {
		t.Fatalf("the holder got the off notice: %s", out)
	}
	if _, out, _ := hook(`{"session_id":"sess-D"}`); out != "" {
		t.Fatalf("a session that never saw the claim got the off notice: %s", out)
	}
	// The witness folder cannot be written: exit 1, the cause last on stderr.
	dir2 := t.TempDir()
	env["PIGTAIL_MAINTENANCE_DIR"] = dir2
	if _, err := runtimeclaim.Claim(dir2, runtimeclaim.State{Session: "sess-A", Reason: "r"}, false, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, runtimeclaim.SeenDirName), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, _, errs = hook(b)
	lines := strings.Split(strings.TrimSpace(errs), "\n")
	if rc != 1 || !strings.Contains(lines[len(lines)-1], runtimeclaim.SeenDirName) {
		t.Fatalf("unwritable witness dir: rc=%d err=%s", rc, errs)
	}
}

func TestRuntimeClaimHookOffSaysExpired(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	getenv := func(k string) string { return env[k] }
	st, err := runtimeclaim.Claim(dir, runtimeclaim.State{Session: "sess-A", Reason: "acceptance", Branch: "feat/a", SHA: shaA},
		false, nil, time.Now().Add(-5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	hook := func() (phase, ctx string) {
		var out bytes.Buffer
		runRuntimeClaimHook(strings.NewReader(`{"session_id":"sess-B","hook_event_name":"UserPromptSubmit"}`), &out, &bytes.Buffer{}, getenv, time.Now)
		if out.Len() == 0 {
			return "", ""
		}
		var v struct {
			H struct {
				Ctx string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(v.H.Ctx, `phase="off"`) {
			return "off", v.H.Ctx
		}
		return "on", v.H.Ctx
	}
	if p, _ := hook(); p != "on" {
		t.Fatalf("first phase = %q, want on", p)
	}
	if _, ok, err := runtimeclaim.Expire(dir, time.Now()); !ok || err != nil {
		t.Fatalf("Expire = %v %v", ok, err)
	}
	p, ctx := hook()
	if p != "off" || !strings.Contains(ctx, "expired") || !strings.Contains(ctx, st.ID) || strings.Contains(ctx, "released") {
		t.Fatalf("off notice = %q (%s)", ctx, p)
	}
	if p, ctx := hook(); p != "" {
		t.Fatalf("again must be silent: %s", ctx)
	}
}

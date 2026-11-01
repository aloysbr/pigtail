package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/testutil"
)

const hoSID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func exists(p string) bool { _, err := os.Stat(p); return err == nil }

type hoFix struct{ srcDir, dstDir, srcPath, dstPath string }

// newHoFix: cells t (work, source) and mt (dstScope, authenticated target);
// hoSID under t launched in /w/orig (slug -w-orig), with a sidecar file;
// private XDG dirs; no running Claude.
func newHoFix(t *testing.T, dstScope string) hoFix {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := hoFix{srcDir: t.TempDir(), dstDir: t.TempDir()}
	writeCells(t, "t: { config_dir: "+f.srcDir+", scope: globex }", "mt: { config_dir: "+f.dstDir+", scope: "+dstScope+" }")
	must(t, os.WriteFile(filepath.Join(f.dstDir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"a@b.c"}}`), 0o600))
	f.srcPath = seedHo(t, f.srcDir)
	f.dstPath = filepath.Join(f.dstDir, "projects", "-w-orig", hoSID+".jsonl")
	stubRunning(t, false)
	return f
}

func writeCells(t *testing.T, cells ...string) {
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pigtail")
	must(t, os.MkdirAll(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte("cells:\n  "+strings.Join(cells, "\n  ")+"\n"), 0o600))
}

func seedHo(t *testing.T, cfg string) string {
	dir := filepath.Join(cfg, "projects", "-w-orig")
	must(t, os.MkdirAll(filepath.Join(dir, hoSID, "subagents"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, hoSID, "subagents", "s.jsonl"), []byte("{}\n"), 0o600))
	p := filepath.Join(dir, hoSID+".jsonl")
	must(t, os.WriteFile(p, []byte("{\"type\":\"mode\",\"cwd\":null}\n{\"type\":\"user\",\"cwd\":\"/w/orig\"}\n"), 0o600))
	return p
}

func stubRunning(t *testing.T, running bool) {
	testutil.SerialOnly(t) // swaps runningProbe
	saved := runningProbe
	t.Cleanup(func() { runningProbe = saved })
	runningProbe = func(string) bool { return running }
}

// writeLivePidFile: cfg/sessions/<this pid>.json names hoSID — a live Claude
// seen through its pid file only, nothing in argv.
func writeLivePidFile(t *testing.T, cfg string) {
	pid := strconv.Itoa(os.Getpid())
	must(t, os.MkdirAll(filepath.Join(cfg, "sessions"), 0o700))
	must(t, os.WriteFile(filepath.Join(cfg, "sessions", pid+".json"), []byte(`{"pid":`+pid+`,"sessionId":"`+hoSID+`"}`), 0o600))
}

func appendLine(t *testing.T, p string) {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteString("{\"type\":\"user\",\"n\":2}\n")
	must(t, err)
	must(t, f.Close())
}

// runHo drives the real CLI; checks handover=1 comes first; returns kv, path= lines, exit code.
func runHo(t *testing.T, args ...string) (map[string]string, []string, int) {
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"session", "handover"}, args...))
	code := exitCode(root.Execute())
	if !strings.HasPrefix(out.String(), "handover=1\n") {
		t.Errorf("stdout %q: handover=1 must come first", out.String())
	}
	kv, paths := map[string]string{}, []string(nil)
	for sc := bufio.NewScanner(&out); sc.Scan(); {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && k == "path" {
			paths = append(paths, v)
		} else if ok {
			kv[k] = v
		}
	}
	return kv, paths, code
}

// runHoFull is runHo keeping every value of each key (removed=, would_remove=
// and path= repeat), plus stderr.
func runHoFull(t *testing.T, args ...string) (map[string][]string, string, int) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"session", "handover"}, args...))
	code := exitCode(root.Execute())
	if !strings.HasPrefix(out.String(), "handover=1\n") {
		t.Errorf("stdout %q: handover=1 must come first", out.String())
	}
	kv := map[string][]string{}
	for sc := bufio.NewScanner(&out); sc.Scan(); {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			kv[k] = append(kv[k], v)
		}
	}
	return kv, errb.String(), code
}

// diverge rewrites p as a fork of seedHo's transcript: same first line, a
// different (longer) second one — neither is a prefix of the other.
func diverge(t *testing.T, p string) {
	t.Helper()
	must(t, os.WriteFile(p, []byte("{\"type\":\"mode\",\"cwd\":null}\n{\"type\":\"user\",\"cwd\":\"/w/orig\",\"x\":1}\n"), 0o600))
}

func size(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	must(t, err)
	return fi.Size()
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

func TestHandoverDryRunReportsWithoutTouching(t *testing.T) {
	f := newHoFix(t, "globex")
	kv, _, code := runHo(t, hoSID, "--to", "mt", "--dry-run")
	for k, v := range map[string]string{"sid": hoSID, "src_cell": "t", "src_scope": "globex", "dst_cell": "mt", "dst_scope": "globex",
		"scope_change": "0", "cwd": "/w/orig", "src_path": f.srcPath, "dst_path": f.dstPath} {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
	if code != 0 || kv["moved"] != "" || exists(f.dstPath) || !exists(f.srcPath) {
		t.Fatalf("dry run: code %d, touched", code)
	}
}

func TestHandoverMovesTranscriptAndSidecar(t *testing.T) {
	f := newHoFix(t, "globex")
	if kv, _, code := runHo(t, hoSID, "--to", "mt"); code != 0 || kv["moved"] != "1" {
		t.Fatalf("code %d, kv %v", code, kv)
	}
	if !exists(f.dstPath) || !exists(filepath.Join(filepath.Dir(f.dstPath), hoSID, "subagents", "s.jsonl")) {
		t.Fatal("target incomplete")
	}
	if exists(f.srcPath) || exists(filepath.Join(filepath.Dir(f.srcPath), hoSID)) {
		t.Fatal("source must be gone")
	}
}

func TestHandoverScopeChange(t *testing.T) {
	f := newHoFix(t, "perso")
	if kv, _, code := runHo(t, hoSID, "--to", "mt", "--dry-run"); code != 0 || kv["scope_change"] != "1" || kv["dst_scope"] != "perso" {
		t.Fatalf("dry run: %d %v", code, kv)
	}
	if _, _, code := runHo(t, hoSID, "--to", "mt"); code != exitHandoverScopeChange || exists(f.dstPath) {
		t.Fatalf("no flag: code %d", code)
	}
	if _, _, code := runHo(t, hoSID, "--to", "mt", "--allow-scope-change"); code != 0 || !exists(f.dstPath) || exists(f.srcPath) {
		t.Fatalf("flag: code %d", code)
	}
}

func TestHandoverReportsEmptyCwd(t *testing.T) {
	f := newHoFix(t, "globex")
	must(t, os.WriteFile(f.srcPath, []byte("{\"cwd\":\"/elsewhere\"}\n"), 0o600))
	if kv, _, code := runHo(t, hoSID, "--to", "mt", "--dry-run"); code != 0 || kv["cwd"] != "" || kv["sid"] != hoSID {
		t.Fatalf("code %d, kv %v", code, kv)
	}
}

// D1=B: several copies, none on the target. One covering the others is moved
// there, the others removed; a fork stays 4 (P4's order kept).
func TestHandoverCopiesElsewhereNoTarget(t *testing.T) {
	t.Run("identical same scope - found order", func(t *testing.T) {
		f := newHoFix(t, "globex")
		other := t.TempDir()
		writeCells(t, "t: { config_dir: "+f.srcDir+", scope: globex }", "mt: { config_dir: "+f.dstDir+", scope: globex }", "jt: { config_dir: "+other+", scope: globex }")
		otherPath := seedHo(t, other)
		kv, _, code := runHoFull(t, hoSID, "--to", "mt")
		// FindAll follows tbl.ConfigDirs(), sorted by cell id: jt before t.
		if code != 0 || strings.Join(kv["moved"], "") != "1" || strings.Join(kv["src_cell"], "") != "jt" ||
			strings.Join(kv["removed"], " ") != f.srcPath || !exists(f.dstPath) || exists(f.srcPath) || exists(otherPath) {
			t.Fatalf("code %d, kv %v", code, kv)
		}
	})
	t.Run("fork", func(t *testing.T) {
		f := newHoFix(t, "globex")
		other := t.TempDir()
		writeCells(t, "t: { config_dir: "+f.srcDir+", scope: globex }", "mt: { config_dir: "+f.dstDir+", scope: globex }", "jt: { config_dir: "+other+", scope: globex }")
		otherPath := seedHo(t, other)
		diverge(t, otherPath)
		kv, _, code := runHoFull(t, hoSID, "--to", "mt")
		if code != exitHandoverAmbiguous || strings.Join(kv["path"], " ") != otherPath+" "+f.srcPath || exists(f.dstPath) || !exists(f.srcPath) || !exists(otherPath) {
			t.Fatalf("code %d, kv %v", code, kv)
		}
	})
	t.Run("identical - the target scope preferred", func(t *testing.T) {
		f := newHoFix(t, "globex")
		other := t.TempDir()
		writeCells(t, "t: { config_dir: "+f.srcDir+", scope: globex }", "mt: { config_dir: "+f.dstDir+", scope: globex }", "jt: { config_dir: "+other+", scope: perso }")
		otherPath := seedHo(t, other)
		kv, _, code := runHoFull(t, hoSID, "--to", "mt")
		if code != 0 || strings.Join(kv["src_cell"], "") != "t" || strings.Join(kv["scope_change"], "") != "0" ||
			strings.Join(kv["removed"], " ") != otherPath || !exists(f.dstPath) || exists(f.srcPath) || exists(otherPath) {
			t.Fatalf("code %d, kv %v", code, kv)
		}
	})
}

// Criterion 10: two cells on one transcript file (a symlinked config dir) —
// "removing" one copy would remove the other. Refused, nothing touched.
func TestHandoverSharedFile(t *testing.T) {
	for _, to := range []string{"mt", "lt"} {
		t.Run(to, func(t *testing.T) {
			f := newHoFix(t, "globex")
			link := filepath.Join(t.TempDir(), "l")
			must(t, os.Symlink(f.srcDir, link))
			writeCells(t, "t: { config_dir: "+f.srcDir+", scope: globex }", "lt: { config_dir: "+link+", scope: globex }", "mt: { config_dir: "+f.dstDir+", scope: globex }")
			_, _, code := runHoFull(t, hoSID, "--to", to)
			if code != 1 || !exists(f.srcPath) || exists(f.dstPath) {
				t.Fatalf("code %d (want 1), shared file kept %v, copied %v", code, exists(f.srcPath), exists(f.dstPath))
			}
		})
	}
}

// One copy holds the others (an older copy is a byte-prefix of it, sidecar
// included): it wins, the others are removed — on the target (5) or then moved
// there (0). Every refusal falls before any removal.
func TestHandoverPrefixCopies(t *testing.T) {
	srcSub := func(f hoFix) string { return filepath.Join(filepath.Dir(f.srcPath), hoSID) }
	dstSub := func(f hoFix) string { return filepath.Join(filepath.Dir(f.dstPath), hoSID) }
	stub := func(t *testing.T, p *func(claudesession.Transcript) error) {
		saved := *p
		t.Cleanup(func() { *p = saved })
		*p = func(claudesession.Transcript) error { return os.ErrPermission }
	}
	// Subtest names reach t.TempDir paths, which writeCells puts unquoted in a
	// YAML flow mapping: no comma nor colon in them.
	for _, c := range []struct {
		name  string
		scope string
		args  []string
		setup func(t *testing.T, f hoFix)
		check func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64)
	}{
		{"head on the target", "globex", nil, func(t *testing.T, f hoFix) { appendLine(t, f.dstPath) },
			func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, _ int64) {
				if code != exitHandoverAlreadyThere || strings.Join(kv["removed"], " ") != f.srcPath || exists(f.srcPath) || exists(srcSub(f)) ||
					size(t, f.dstPath) != dstSize || !strings.Contains(firstLine(stderr), "1 covered") {
					t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
				}
			}},
		{"head on the target - dry run", "globex", []string{"--dry-run"}, func(t *testing.T, f hoFix) { appendLine(t, f.dstPath) },
			func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
				if code != exitHandoverAlreadyThere || strings.Join(kv["would_remove"], " ") != f.srcPath || len(kv["removed"]) != 0 ||
					size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize || !exists(srcSub(f)) {
					t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
				}
			}},
		{"head elsewhere (the target holds the stale copy)", "globex", nil, func(t *testing.T, f hoFix) { appendLine(t, f.srcPath) },
			func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, _, srcSize int64) {
				if code != 0 || strings.Join(kv["moved"], "") != "1" || strings.Join(kv["removed"], " ") != f.dstPath ||
					size(t, f.dstPath) != srcSize || exists(f.srcPath) || exists(srcSub(f)) || !exists(dstSub(f)) ||
					!strings.HasPrefix(stderr, "session "+hoSID[:8]+" moved t → mt\n") {
					t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
				}
			}},
		{"head elsewhere - dry run", "globex", []string{"--dry-run"}, func(t *testing.T, f hoFix) { appendLine(t, f.srcPath) },
			func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
				if code != 0 || strings.Join(kv["would_remove"], " ") != f.dstPath || strings.Join(kv["src_cell"], "") != "t" ||
					len(kv["moved"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize {
					t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
				}
			}},
		{"head's sidecar is a superset", "globex", nil, func(t *testing.T, f hoFix) {
			appendLine(t, f.srcPath)
			must(t, os.WriteFile(filepath.Join(srcSub(f), "subagents", "s2.jsonl"), []byte("{}\n"), 0o600))
		}, func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, _, _ int64) {
			if code != 0 || strings.Join(kv["moved"], "") != "1" || !exists(filepath.Join(dstSub(f), "subagents", "s2.jsonl")) {
				t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
			}
		}},
		{"real fork", "globex", nil, func(t *testing.T, f hoFix) { diverge(t, f.dstPath) },
			func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
				if code != exitHandoverAmbiguous || len(kv["path"]) != 2 || len(kv["removed"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize {
					t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
				}
			}},
		{"sidecar fork", "globex", nil, func(t *testing.T, f hoFix) {
			appendLine(t, f.srcPath)
			must(t, os.WriteFile(filepath.Join(dstSub(f), "subagents", "s.jsonl"), []byte("{\"z\":9}\n"), 0o600))
		}, func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
			if code != exitHandoverAmbiguous || len(kv["removed"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize {
				t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
			}
		}},
		{"head elsewhere - session running", "globex", nil, func(t *testing.T, f hoFix) {
			appendLine(t, f.srcPath)
			writeLivePidFile(t, f.srcDir)
		}, func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
			if code != exitHandoverRunning || len(kv["removed"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize {
				t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
			}
		}},
		{"head elsewhere - scope refused", "perso", nil, func(t *testing.T, f hoFix) { appendLine(t, f.srcPath) },
			func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
				if code != exitHandoverScopeChange || len(kv["removed"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize {
					t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
				}
			}},
		{"head elsewhere - target not authenticated", "globex", nil, func(t *testing.T, f hoFix) {
			appendLine(t, f.srcPath)
			must(t, os.Remove(filepath.Join(f.dstDir, ".claude.json")))
		}, func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
			if code != 1 || len(kv["removed"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize {
				t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
			}
		}},
		{"head elsewhere - removal fails", "globex", nil, func(t *testing.T, f hoFix) {
			appendLine(t, f.srcPath)
			stub(t, &removeSource)
		}, func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, dstSize, srcSize int64) {
			if code != 1 || len(kv["moved"]) != 0 || len(kv["removed"]) != 0 || size(t, f.srcPath) != srcSize || size(t, f.dstPath) != dstSize ||
				!strings.Contains(firstLine(stderr), "remove the stale copy") {
				t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
			}
		}},
		{"head elsewhere - copy fails after the removal", "globex", nil, func(t *testing.T, f hoFix) {
			appendLine(t, f.srcPath)
			saved := copyTranscript
			t.Cleanup(func() { copyTranscript = saved })
			copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
				return claudesession.Transcript{}, os.ErrPermission
			}
		}, func(t *testing.T, f hoFix, kv map[string][]string, stderr string, code int, _, srcSize int64) {
			if code != 1 || strings.Join(kv["removed"], " ") != f.dstPath || len(kv["moved"]) != 0 || size(t, f.srcPath) != srcSize ||
				exists(f.dstPath) || !strings.Contains(firstLine(stderr), "copy:") {
				t.Fatalf("code %d, kv %v, stderr %q", code, kv, stderr)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoFix(t, c.scope)
			seedHo(t, f.dstDir)
			c.setup(t, f)
			dstSize, srcSize := size(t, f.dstPath), size(t, f.srcPath)
			kv, stderr, code := runHoFull(t, append([]string{hoSID, "--to", "mt"}, c.args...)...)
			c.check(t, f, kv, stderr, code, dstSize, srcSize)
		})
	}
}

// Every refusal: its code, nothing copied, source untouched.
func TestHandoverRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f hoFix) (args []string)
		want  int
	}{
		{"not found", func(*testing.T, hoFix) []string {
			return []string{"bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee", "--to", "mt"}
		}, exitHandoverNotFound},
		{"argv", func(t *testing.T, _ hoFix) []string { stubRunning(t, true); return nil }, exitHandoverRunning},
		{"pidfile", func(t *testing.T, f hoFix) []string { writeLivePidFile(t, f.srcDir); return nil }, exitHandoverRunning},
		{"unauthenticated", func(t *testing.T, f hoFix) []string {
			must(t, os.Remove(filepath.Join(f.dstDir, ".claude.json")))
			return nil
		}, 1},
		{"not a uuid", func(*testing.T, hoFix) []string { return []string{"mon-titre", "--to", "mt"} }, 1},
		{"copy fails", func(t *testing.T, _ hoFix) []string {
			saved := copyTranscript
			t.Cleanup(func() { copyTranscript = saved })
			copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
				return claudesession.Transcript{}, os.ErrPermission
			}
			return nil
		}, 1},
		{"source written during the copy", func(t *testing.T, f hoFix) []string {
			saved := copyTranscript
			t.Cleanup(func() { copyTranscript = saved })
			copyTranscript = func(tr claudesession.Transcript, dst string) (claudesession.Transcript, error) {
				moved, err := claudesession.Copy(tr, dst)
				appendLine(t, f.srcPath)
				return moved, err
			}
			return nil
		}, 1},
		{"lock held", func(t *testing.T, _ hoFix) []string {
			unlock, err := claudesession.LockSession(hoSID)
			must(t, err)
			t.Cleanup(unlock)
			return nil
		}, exitHandoverBusy},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoFix(t, "globex")
			args := c.setup(t, f)
			if args == nil {
				args = []string{hoSID, "--to", "mt"}
			}
			if _, _, code := runHo(t, args...); code != c.want || exists(f.dstPath) || !exists(f.srcPath) {
				t.Fatalf("code %d (want %d); copied %v, source kept %v", code, c.want, exists(f.dstPath), exists(f.srcPath))
			}
		})
	}
}

func TestHandoverDryRunTakesNoLock(t *testing.T) {
	newHoFix(t, "globex")
	unlock, err := claudesession.LockSession(hoSID)
	must(t, err)
	defer unlock()
	if _, _, code := runHo(t, hoSID, "--to", "mt", "--dry-run"); code != 0 {
		t.Fatalf("code %d, want 0", code)
	}
}

func TestHandoverAlreadyOnTarget(t *testing.T) {
	f := newHoFix(t, "globex")
	must(t, os.MkdirAll(filepath.Join(f.dstDir, "projects"), 0o700))
	must(t, os.Rename(filepath.Dir(f.srcPath), filepath.Join(f.dstDir, "projects", "-w-orig")))
	// D2 on the D8 path: the launcher needs the launch dir even when nothing moves.
	if kv, _, code := runHo(t, hoSID, "--to", "mt", "--dry-run"); code != exitHandoverAlreadyThere || kv["cwd"] != "/w/orig" || kv["dst_cell"] != "mt" {
		t.Fatalf("code %d, kv %v; want 5 with cwd=/w/orig", code, kv)
	}
	writeLivePidFile(t, f.dstDir) // P14: open in another tab
	if _, _, code := runHo(t, hoSID, "--to", "mt"); code != exitHandoverRunning {
		t.Fatalf("running: code %d, want 6", code)
	}
}

// D10: a move killed between copy and removal leaves two identical copies;
// the next call keeps the target's and removes the other.
func TestHandoverSecondCallSettlesAKilledMove(t *testing.T) {
	f := newHoFix(t, "globex")
	saved := removeSource
	t.Cleanup(func() { removeSource = saved })
	removeSource = func(claudesession.Transcript) error { return os.ErrPermission } // stands for the kill
	if _, _, code := runHo(t, hoSID, "--to", "mt"); code != 1 || !exists(f.srcPath) || !exists(f.dstPath) {
		t.Fatalf("1st call: code %d", code)
	}
	removeSource = saved
	if kv, _, code := runHo(t, hoSID, "--to", "mt"); code != exitHandoverAlreadyThere || kv["removed"] != f.srcPath || exists(f.srcPath) || !exists(f.dstPath) {
		t.Fatalf("2nd call: code %d, kv %v", code, kv)
	}
}

// Copy on the target + another: identical ⇒ removed (dry run: only listed);
// divergent ⇒ 4, both kept; running ⇒ 6, both kept.
func TestHandoverDuplicates(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		alter   func(t *testing.T, f hoFix)
		want    int
		srcKept bool
		key     string
	}{
		{"dry run", []string{"--dry-run"}, nil, exitHandoverAlreadyThere, true, "would_remove"},
		{"divergent", nil, func(t *testing.T, f hoFix) { diverge(t, f.dstPath) }, exitHandoverAmbiguous, true, ""},
		{"running", nil, func(t *testing.T, f hoFix) { writeLivePidFile(t, f.srcDir) }, exitHandoverRunning, true, ""},
		{"identical", nil, nil, exitHandoverAlreadyThere, false, "removed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHoFix(t, "globex")
			seedHo(t, f.dstDir)
			if c.alter != nil {
				c.alter(t, f)
			}
			kv, _, code := runHo(t, append([]string{hoSID, "--to", "mt"}, c.args...)...)
			if code != c.want || exists(f.srcPath) != c.srcKept || !exists(f.dstPath) || (c.key != "" && kv[c.key] != f.srcPath) {
				t.Fatalf("code %d (want %d), source kept %v, kv %v", code, c.want, exists(f.srcPath), kv)
			}
		})
	}
}

// The contract holds on every invocation: handover=1 first on stdout, even
// when cobra would refuse the arguments or flags before RunE — the launcher
// reads its absence as "pigtail too old".
func TestHandoverAnnouncesItselfOnArgumentErrors(t *testing.T) {
	newHoFix(t, "globex")
	for name, args := range map[string][]string{
		"no --to":      {hoSID},
		"two args":     {hoSID, hoSID, "--to", "mt"},
		"unknown flag": {hoSID, "--to", "mt", "--nope"},
		"no args":      {"--to", "mt"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, code := runHo(t, args...); code != 1 {
				t.Fatalf("code %d, want 1", code)
			}
		})
	}
}

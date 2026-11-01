package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemonwatch"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail daemon watchdog`: run every minute by the sh.pigtail.watchdog
// LaunchAgent. It probes GET /health?deep=1, and a pigtaild that stays alive
// without answering is kicked (launchctl kickstart -k), at most 3 times an
// hour. It exits 0 on every probe outcome: launchd must not throttle it.

// watchdogKickStarting: a process that holds the lock younger than this is
// still migrating (migrations run after the lock and before the socket binds).
const watchdogKickStarting = 10 * time.Minute

var errWatchdogStale = errors.New("the watchdog has not run for over 3 minutes (is sh.pigtail.watchdog loaded?)")

// watchdogRunner is the launchctl runner; tests swap in a fake.
var watchdogRunner setup.Runner = setup.ExecRunner

// watchdogProbeTimeout bounds the health probe; tests shorten it.
var watchdogProbeTimeout = 5 * time.Second

func daemonWatchdogCmd() *cobra.Command {
	var db string
	var status, crossScope bool
	cmd := &cobra.Command{
		Use:         "watchdog",
		Annotations: scopedWrite(),
		Short:       "Probe pigtaild and kick it when it stays wedged (run by the sh.pigtail.watchdog LaunchAgent)",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if db == "" {
				if flagSocket == "" && os.Getenv("PIGTAIL_SOCKET") == "" {
					db = filepath.Join(setup.ConfigDir(), "pigtail.db")
				} else {
					db = filepath.Join(filepath.Dir(socketPath()), "pigtail.db")
				}
			}
			if flagSocket == "" && os.Getenv("PIGTAIL_SOCKET") == "" && flagHTTP == "" {
				flagSocket = filepath.Join(filepath.Dir(db), "pigtail.sock")
			}
			statePath := filepath.Join(filepath.Dir(spawnreg.DefaultPath()), "daemon-watchdog.json")
			if status {
				return watchdogStatus(cmd.OutOrStdout(), statePath, time.Now())
			}
			post := func(es []daemonwatch.Event) error {
				c, err := newScopedClient()
				if err != nil {
					return err
				}
				if crossScope {
					c = c.WithCrossScopeWrite()
				}
				cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				for _, e := range es {
					if _, err := c.AddJournal(cx, watchdogEntry(e)); err != nil {
						return err
					}
				}
				return nil
			}
			return daemonwatch.Run(context.Background(), watchdogDeps(db, statePath, post, cmd.ErrOrStderr()))
		},
	}
	cmd.Flags().StringVar(&db, "db", "", "pigtaild's database path (default: next to the socket)")
	cmd.Flags().BoolVar(&status, "status", false, "print the watchdog's state as JSON; exit 1 when it has not run for 3 minutes")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func watchdogStatus(w io.Writer, statePath string, now time.Time) error {
	st, err := daemonwatch.LoadState(statePath)
	if err != nil {
		return err
	}
	stale := daemonwatch.StaleRun(st, now)
	b, err := json.MarshalIndent(struct {
		daemonwatch.State
		Stale bool `json:"stale"`
	}{st, stale}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(b))
	if stale {
		return errWatchdogStale
	}
	return nil
}

func watchdogDeps(db, statePath string, post func([]daemonwatch.Event) error, errw io.Writer) daemonwatch.Deps {
	getenv := os.Getenv
	return daemonwatch.Deps{
		Probe: func(ctx context.Context) (daemonwatch.Result, string) {
			return watchdogProbe(ctx, socketPathOrDefault(db), db, time.Now())
		},
		Kick:        func(ctx context.Context) error { return setup.Kickstart(ctx, watchdogRunner) },
		KickTimeout: 30 * time.Second,
		Post:        post,
		Maintenance: func() (bool, error) {
			st, ok, err := maintenance.Load(maintenanceDir(getenv))
			return ok && st.On, err
		},
		RuntimeClaim: func() (bool, error) {
			st, ok, err := runtimeclaim.Load(runtimeClaimDir(getenv))
			// A lease past its deadline never holds the watchdog: only a
			// wedged pigtaild would fail to lift it.
			return ok && !st.Expired(time.Now()), err
		},
		Load: func() (daemonwatch.State, error) { return daemonwatch.LoadState(statePath) },
		Save: func(s daemonwatch.State) error { return daemonwatch.SaveState(statePath, s) },
		Now:  time.Now,
		Logf: func(f string, a ...any) { fmt.Fprintf(errw, "daemon-watchdog: "+f+"\n", a...) },
	}
}

func socketPathOrDefault(db string) string {
	if s := socketPath(); s != "" {
		return s
	}
	return filepath.Join(filepath.Dir(db), "pigtail.sock")
}

// watchdogProbe asks the daemon on sock for GET /health?deep=1.
func watchdogProbe(ctx context.Context, sock, db string, now time.Time) (daemonwatch.Result, string) {
	hc := &http.Client{
		Timeout: watchdogProbeTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://pigtail/health?deep=1", nil)
	if err != nil {
		return daemonwatch.Wedged, err.Error()
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return watchdogRefused(db, now)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return daemonwatch.Wedged, "timeout"
		}
		return daemonwatch.Wedged, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
		return daemonwatch.Healthy, ""
	case http.StatusServiceUnavailable:
		var out struct {
			Data struct {
				Failing []string `json:"failing"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &out)
		return daemonwatch.Wedged, fmt.Sprintf("503 failing=%v", out.Data.Failing)
	}
	return daemonwatch.Wedged, fmt.Sprintf("status %d", resp.StatusCode)
}

// watchdogRefused tells a dead daemon from a starting or wedged one by the pid
// the instance lock holds (<db>.lock, written at acquisition).
func watchdogRefused(db string, now time.Time) (daemonwatch.Result, string) {
	lock := db + ".lock"
	b, err := os.ReadFile(lock)
	if err != nil {
		return daemonwatch.Down, "no lock file"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return daemonwatch.Down, "no pid in the lock file"
	}
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return daemonwatch.Down, fmt.Sprintf("pid %d is gone", pid)
	}
	fi, err := os.Stat(lock)
	if err == nil && now.Sub(fi.ModTime()) < watchdogKickStarting {
		return daemonwatch.Starting, fmt.Sprintf("pid %d started %s ago", pid, now.Sub(fi.ModTime()).Round(time.Second))
	}
	return daemonwatch.Wedged, fmt.Sprintf("refused while pid %d alive", pid)
}

// watchdogEntry is the journal row of one event: id daemon-watchdog:<kind>:<ms>.
func watchdogEntry(e daemonwatch.Event) pigtail.JournalEntry {
	var summary string
	switch e.Kind {
	case "gave-up":
		summary = "pigtaild watchdog gave up: " + e.Detail
	default:
		summary = "pigtaild kicked: " + e.Detail
	}
	return pigtail.JournalEntry{
		ID:        fmt.Sprintf("daemon-watchdog:%s:%d", e.Kind, e.At.UnixMilli()),
		Kind:      "daemon-watchdog",
		Summary:   clipSummary(summary),
		Source:    "pigtail:daemon-watchdog?kind=" + e.Kind,
		Timestamp: e.At,
	}
}

// clipSummary makes a detail one line of at most JournalSummaryMax runes.
func clipSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	r := []rune(s)
	if len(r) > pigtail.JournalSummaryMax {
		s = string(r[:pigtail.JournalSummaryMax-1]) + "…"
	}
	return s
}

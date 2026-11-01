package cmuxcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// NoticeFile: the end-of-pass notice (D2, sub_x000000000000037), written by
// pigtaild once the last pass of a cmux launch is done; read by
// GET /cmux/check/notice, posted by the Pigtail app. Master-only, like last.json.
const NoticeFile = "notice.json"

type Notice struct {
	LaunchPID int       `json:"launch_pid"`
	At        time.Time `json:"at"`
	Line      string    `json:"line"` // "" = nothing for the user: no notification (D2)
}

const noticeMaxNamed = 3

// NoticeLine (D2): one line when something is left for the user, else "". A
// row is named by its workspace's title, else its pane's (noticeName), never
// an id; three at most, then "+N".
func NoticeLine(r Report) string {
	if r.Fatal != "" {
		return "Reprise cmux impossible (" + cmux.OneLine(r.Fatal, 80) + ") — pigtail cmux check --fix"
	}
	n := 0
	var left []string
	for _, row := range r.Rows {
		switch {
		case row.Category() == CatRepaired:
			n++
		case row.Remaining && !row.ClosedOrphan:
			left = append(left, noticeName(row)+" "+leftWhy(row))
		}
	}
	if len(left) == 0 {
		return ""
	}
	var parts []string
	if n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "relancée", "relancées")))
	}
	named := left
	if len(named) > noticeMaxNamed {
		named = left[:noticeMaxNamed]
	}
	line := strings.Join(append(parts, named...), " · ")
	if extra := len(left) - len(named); extra > 0 {
		line += fmt.Sprintf(" +%d", extra)
	}
	return line + " — pigtail cmux check --last"
}

// noticeName: the row's workspace title, else its pane's, else a neutral
// word — never Row.Name()'s ShortID fallback (an id in the banner).
func noticeName(row Row) string {
	switch {
	case row.WorkspaceTitle != "":
		return row.WorkspaceTitle
	case row.Title != "":
		return row.Title
	}
	return "une session"
}

// leftWhy: what the row waits on, in the user's words. A start stopped on a
// dialog lands in Result (« start failed: stopped on a dialog … »), not Blocked.
func leftWhy(row Row) string {
	switch {
	case strings.Contains(row.Blocked, "busy: status waiting"), strings.Contains(row.Blocked, "dialog"),
		strings.Contains(row.Result, "stopped on a dialog"):
		return "attend ta réponse"
	case strings.Contains(row.Blocked, "busy: "+focusReason):
		return "au premier plan, à lancer"
	case strings.Contains(row.Blocked, "bypassPermissions"):
		return "à lancer à la main"
	case busyBlocked(row.Blocked):
		return "occupée"
	case row.Result != "":
		return "relance ratée"
	case row.Blocked == "" && len(row.Actions) == 0:
		return "tâche de fond perdue"
	}
	return "à voir"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func SaveNotice(dir string, n Notice) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, NoticeFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, NoticeFile))
}

func LoadNotice(dir string) (Notice, error) {
	var n Notice
	b, err := os.ReadFile(filepath.Join(dir, NoticeFile))
	if err != nil {
		return n, err
	}
	return n, json.Unmarshal(b, &n)
}

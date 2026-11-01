package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// backfillURLRE finds http(s) URLs in a free-text field (the "items" lines).
var backfillURLRE = regexp.MustCompile(`https?://[^\s"'<>]+`)

// backfillLocalRE matches a URL served by a local static server.
var backfillLocalRE = regexp.MustCompile(`^https?://(?:127\.0\.0\.1|localhost):(\d+)(/[^?#]*)?(?:[?#].*)?$`)

// openEntry is one line of a claude-opens journal, the fields the backfill reads.
type openEntry struct {
	Type      string          `json:"type"`
	SubjectID string          `json:"subject_id"`
	Subject   string          `json:"subject"`
	Target    string          `json:"target"`
	IsPath    bool            `json:"is_path"`
	Items     string          `json:"items"`
	Argv      json.RawMessage `json:"argv"`
	TS        float64         `json:"ts"`
}

func (e openEntry) subjectID() string {
	if s := strings.TrimSpace(e.SubjectID); s != "" {
		return s
	}
	return strings.TrimSpace(e.Subject)
}

// targets lists what the entry opened: target, else every URL of items, else
// the URL of argv. argv is a JSON array of strings (older lines may carry a
// string; anything else yields no URL).
func (e openEntry) targets() []string {
	if t := strings.TrimSpace(e.Target); t != "" {
		return []string{t}
	}
	if u := backfillURLRE.FindAllString(e.Items, -1); len(u) > 0 {
		return u
	}
	var argv []string
	if json.Unmarshal(e.Argv, &argv) == nil {
		for _, a := range argv {
			if backfillURLRE.MatchString(a) {
				return []string{a}
			}
		}
	}
	return nil
}

// resolveBackfillTarget maps a journal target to a file on disk: a path as is;
// a local server URL through its port's dir (index.html for a folder). ok=false:
// unknown port, foreign URL, or nothing on disk.
func resolveBackfillTarget(target string, isPath bool, dirs map[int]string) (string, bool) {
	var p string
	switch m := backfillLocalRE.FindStringSubmatch(target); {
	case m != nil:
		var port int
		if _, err := fmt.Sscanf(m[1], "%d", &port); err != nil {
			return "", false
		}
		root, found := dirs[port]
		if !found {
			return "", false
		}
		rel, err := url.PathUnescape(m[2])
		if err != nil {
			return "", false
		}
		p = filepath.Join(root, filepath.FromSlash(rel))
		// Never leave the server's root (a ".." in a logged URL).
		if r, err := filepath.Rel(root, p); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", false
		}
	case outputSchemeRE.MatchString(target) && !isPath:
		return "", false
	default:
		p = target
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", false
	}
	if st.IsDir() {
		p = filepath.Join(p, "index.html")
		if st, err = os.Stat(p); err != nil || st.IsDir() {
			return "", false
		}
	}
	return filepath.Clean(p), true
}

func subjectOutputBackfillCmd() *cobra.Command {
	var dir string
	var dryRun, crossScope bool
	cmd := &cobra.Command{
		Use:         "backfill [--dir D] [--dry-run]",
		Annotations: scopedWrite(),
		Short:       "Register the mockups and reports opened before the registry existed (from claude-opens)",
		Long: `Read the claude-opens journals (*.jsonl in --dir) and register each mockup and
report they opened on its subject (subject_id, else subject). The file is the
target path, else every URL of items, else the URL of argv; a URL served by a
static server resolves through that server's dir (index.html for a folder).

Prints four counts: registered, skipped (no subject), gone (unknown server or
file absent), already in the registry. Idempotent: a second pass registers 0.
--dry-run counts without writing.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, ".local", "state", "claude-opens")
			}
			if st, err := os.Stat(dir); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("dossier introuvable : %s — passe --dir vers le dossier des journaux claude-opens", dir)
				}
				return fmt.Errorf("dossier illisible : %s (%w) — passe --dir vers le dossier des journaux claude-opens", dir, err)
			} else if !st.IsDir() {
				return fmt.Errorf("ce n'est pas un dossier : %s — passe --dir vers le dossier des journaux claude-opens", dir)
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			servers, err := c.ListStaticServers(cx)
			if err != nil {
				return err
			}
			dirs := map[int]string{}
			for _, s := range servers {
				dirs[s.Port] = s.Dir
			}
			entries, err := readOpenEntries(dir)
			if err != nil {
				return err
			}
			var registered, skipped, missing, already int
			seen := map[string]bool{}
			for _, e := range entries {
				sid := e.subjectID()
				if sid == "" {
					skipped++
					continue
				}
				ts := e.TS
				targets := e.targets()
				if len(targets) == 0 {
					missing++
					continue
				}
				for _, t := range targets {
					p, ok := resolveBackfillTarget(t, e.IsPath, dirs)
					if !ok {
						missing++
						continue
					}
					if k := sid + "\x00" + p; seen[k] {
						continue
					} else {
						seen[k] = true
					}
					if dryRun {
						registered++
						continue
					}
					res, err := c.AddSubjectOutput(cx, pigtail.SubjectOutput{
						SubjectID: sid, Kind: e.Type, Path: p, Title: pigtail.DefaultOutputTitle(p, ""),
						TS: int64(ts * 1000),
					})
					if err != nil {
						if isNotFound(err) {
							skipped++ // a subject this scope cannot see, or gone
							continue
						}
						return err
					}
					if res.Created {
						registered++
					} else {
						already++
					}
				}
			}
			fmt.Printf("inscrites %d\nsautées (sans sujet) %d\ndisparues %d\ndéjà au registre %d\n",
				registered, skipped, missing, already)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "journals directory (default ~/.local/state/claude-opens)")
	f.BoolVar(&dryRun, "dry-run", false, "count only, write nothing")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// readOpenEntries reads every *.jsonl of dir (sorted by name) and keeps the
// mockup and report lines; unreadable or non-JSON lines are ignored.
func readOpenEntries(dir string) ([]openEntry, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []openEntry
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			var e openEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil || (e.Type != "mockup" && e.Type != "report") {
				continue
			}
			out = append(out, e)
		}
		serr := sc.Err()
		_ = fh.Close()
		if serr != nil {
			return nil, fmt.Errorf("%s: %w", f, serr)
		}
	}
	return out, nil
}

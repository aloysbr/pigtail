package claudeusage

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fake account uuids: the real ones never enter the repo.
const (
	uuidM = "11111111-1111-4111-8111-111111111111"
	uuidI = "22222222-2222-4222-8222-222222222222"
	uuidJ = "33333333-3333-4333-8333-333333333333"
	uuidT = "44444444-4444-4444-8444-444444444444"
)

// now0 is the instant of the research reading (2026-10-05 11:32Z).
var now0 = time.Date(2026, 10, 5, 11, 32, 0, 0, time.UTC)

// cellFx fills the holes of testdata/max-cell.json.tmpl.
type cellFx struct {
	UUID, CacheUUID   string // CacheUUID "" = same as UUID
	OrgTier, UserTier string // OrgTier "" = default_claude_max_20x; UserTier "" = null
	Fetched           time.Time
	P7, P5            int
	R7, R5            string // ISO 8601, µs and +00:00, as Claude Code writes them
}

func maxCell(t *testing.T, fx cellFx) []byte {
	t.Helper()
	tmpl, err := os.ReadFile(filepath.Join("testdata", "max-cell.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if fx.CacheUUID == "" {
		fx.CacheUUID = fx.UUID
	}
	if fx.OrgTier == "" {
		fx.OrgTier = "default_claude_max_20x"
	}
	user := "null"
	if fx.UserTier != "" {
		user = strconv.Quote(fx.UserTier)
	}
	return []byte(strings.NewReplacer(
		"{{UUID}}", fx.UUID, "{{CACHE_UUID}}", fx.CacheUUID,
		"{{ORG_TIER}}", fx.OrgTier, "{{USER_TIER}}", user,
		"{{FETCHED_MS}}", strconv.FormatInt(fx.Fetched.UnixMilli(), 10),
		"{{P7}}", strconv.Itoa(fx.P7), "{{P5}}", strconv.Itoa(fx.P5),
		"{{R7}}", fx.R7, "{{R5}}", fx.R5,
	).Replace(string(tmpl)))
}

func teamCell(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "team-cell.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// noCacheCell: a logged-in cell that never fetched usage (cell p today).
func noCacheCell(uuid string) []byte {
	return []byte(`{"oauthAccount":{"accountUuid":"` + uuid + `","organizationRateLimitTier":"default_claude_max_20x","userRateLimitTier":null}}`)
}

// fakeFS serves files by absolute path and records every path asked for.
type fakeFS struct {
	files map[string][]byte
	asked []string
}

func (f *fakeFS) read(p string) ([]byte, error) {
	f.asked = append(f.asked, p)
	b, ok := f.files[p]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return b, nil
}

// cellAt is a CellInput whose config dir is /cells/<id>.
func cellAt(id, scope, account string) CellInput {
	return CellInput{ID: id, ConfigDir: "/cells/" + id, Scope: scope, Account: account}
}

func fileOf(c CellInput) string { return filepath.Join(c.ConfigDir, FileName) }

// snapshots reads every cell, in the given order (the caller passes id order).
func snapshots(cells []CellInput, files map[string][]byte) []Snapshot {
	f := &fakeFS{files: files}
	out := make([]Snapshot, 0, len(cells))
	for _, c := range cells {
		out = append(out, ReadCell(c, f.read))
	}
	return out
}

// researchCells is the 2026-10-05 11:32Z reading of the research, letters set
// (as after a later cell-table change): ip it jp jt mp mt p t, in id order.
func researchCells(t *testing.T) ([]CellInput, map[string][]byte) {
	cells := []CellInput{
		cellAt("ip", "perso", "i"), cellAt("it", "gx", "i"),
		cellAt("jp", "perso", "j"), cellAt("jt", "gx", "j"),
		cellAt("mp", "perso", "m"), cellAt("mt", "gx", "m"),
		cellAt("p", "perso", "m"), cellAt("t", "gx", "t"),
	}
	files := map[string][]byte{
		fileOf(cells[0]): maxCell(t, cellFx{UUID: uuidI, Fetched: now0.Add(-21 * time.Hour), P7: 0, R7: "2026-10-11T08:00:00.411+00:00", P5: 2, R5: "2026-10-04T19:20:00.1+00:00"}),
		fileOf(cells[1]): maxCell(t, cellFx{UUID: uuidI, Fetched: now0.Add(-(15*time.Hour + 12*time.Minute)), P7: 9, R7: "2026-10-11T07:59:59.735093+00:00", P5: 11, R5: "2026-10-05T01:00:00.735+00:00"}),
		fileOf(cells[2]): maxCell(t, cellFx{UUID: uuidJ, Fetched: now0.Add(-time.Minute), P7: 91, R7: "2026-10-06T02:00:00.177378+00:00", P5: 8, R5: "2026-10-05T11:50:00.177348+00:00"}),
		fileOf(cells[3]): maxCell(t, cellFx{UUID: uuidJ, Fetched: now0.Add(-(23*time.Hour + 36*time.Minute)), P7: 73, R7: "2026-10-06T02:00:00.177378+00:00", P5: 25, R5: "2026-10-04T16:00:00.2+00:00"}),
		fileOf(cells[4]): maxCell(t, cellFx{UUID: uuidM, Fetched: now0.Add(-13 * time.Hour), P7: 1, R7: "2026-10-11T17:00:00.512+00:00", P5: 3, R5: "2026-10-05T02:30:00.5+00:00"}),
		fileOf(cells[5]): maxCell(t, cellFx{UUID: uuidM, Fetched: now0.Add(-(12*time.Hour + 36*time.Minute)), P7: 1, R7: "2026-10-11T17:00:00.512+00:00", P5: 4, R5: "2026-10-05T03:00:00.5+00:00"}),
		fileOf(cells[6]): noCacheCell(uuidM),
		fileOf(cells[7]): teamCell(t),
	}
	return cells, files
}

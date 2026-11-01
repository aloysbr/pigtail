package daemon

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/usagehistory"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// usageWeek is the length of the 7-day window.
const usageWeek = 7 * 24 * time.Hour

// usageOverviewSource builds GET /usage/overview from the cell table, the
// readings, the usage history and the sweep's last state — the sweep's own
// seams, so a test drives it without any real file.
type usageOverviewSource struct {
	loadCells func() ([]claudeusage.CellInput, error)
	readFile  func(string) ([]byte, error)
	history   *usagehistory.History
	stateDir  string // where usage-refresh.json lives; "" = no refresh state
}

// newUsageOverviewSource reads the real cell table (setup.ConfigDir) and the
// cells' .claude.json, like the usage sweep.
func newUsageOverviewSource(stateDir string, history *usagehistory.History) *usageOverviewSource {
	return &usageOverviewSource{
		loadCells: func() ([]claudeusage.CellInput, error) {
			tbl, err := claudeprofile.Load(setup.ConfigDir())
			if err != nil {
				return nil, err
			}
			return usageCellInputs(tbl), nil
		},
		readFile: os.ReadFile,
		history:  history,
		stateDir: stateDir,
	}
}

// Overview is the master's view of every account, in Build's order, and the
// account the next session of each declared scope would start on (Pick).
// Nothing in it names a cell (D19): accounts by uuid and letter only.
func (u *usageOverviewSource) Overview(now time.Time) (pigtail.UsageOverview, error) {
	now = now.UTC()
	cells, err := u.loadCells()
	if err != nil {
		return pigtail.UsageOverview{}, fmt.Errorf("cell table: %w", err)
	}
	snaps := claudeusage.ReadTable(cells, u.readFile)
	rep := claudeusage.Build(snaps, scope.Master(), now)
	accts := rep.Accounts
	failed := u.refreshFailed()
	oldest := map[string]time.Time{}
	recent := u.history.Since(now.Add(-usagehistory.Keep))
	for _, s := range recent {
		if t, ok := oldest[s.AccountUUID]; !ok || s.At.Before(t) {
			oldest[s.AccountUUID] = s.At
		}
	}
	ov := pigtail.UsageOverview{GeneratedAt: now, Accounts: make([]pigtail.UsageAccount, 0, len(accts)),
		NextSession: []pigtail.UsageScopePick{}, UnreadableCount: len(rep.Unreadable)}
	for _, a := range accts {
		ua := usageAccount(a, now)
		ua.RefreshFailed = failed[a.AccountUUID]
		if t, ok := oldest[a.AccountUUID]; ok {
			ua.HistorySince = &t
		}
		ov.Accounts = append(ov.Accounts, ua)
	}
	ov.Verdict = usageVerdict(accts, ov.Accounts, recent, now)
	tbl := scope.Active()
	for _, sc := range tbl.Names() {
		p := pigtail.UsageScopePick{Scope: string(sc), Display: string(sc), Master: tbl.SeesAll(sc)}
		if al := tbl.Aliases(sc); len(al) > 0 {
			p.Display = al[0]
		}
		if ch, err := claudeusage.Pick(snaps, claudeusage.PickOptions{Scope: sc}, now); err != nil {
			p.Error = "no-candidate" // never the error's text: it names cells
		} else {
			p.AccountUUID = ch.AccountUUID
		}
		ov.NextSession = append(ov.NextSession, p)
	}
	return ov, nil
}

// usageVerdict runs the week's simulation over the accounts of the verdict,
// each at its own rhythm. accts and shown are parallel (Build's order). With
// no history every rhythm is the provisional mean.
func usageVerdict(accts []claudeusage.Account, shown []pigtail.UsageAccount, history []claudeusage.Sample, now time.Time) *pigtail.UsageVerdict {
	byAcct := map[string][]claudeusage.Sample{}
	for _, s := range history {
		byAcct[s.AccountUUID] = append(byAcct[s.AccountUUID], s)
	}
	var sim []claudeusage.SimAccount
	v := &pigtail.UsageVerdict{}
	for i, a := range accts {
		if !shown[i].InVerdict {
			continue
		}
		rd := a.Reading
		rates := claudeusage.RatesFor(byAcct[a.AccountUUID], rd, now)
		if !rates.FromHistory {
			v.Provisional = true
		}
		sa := claudeusage.SimAccount{Label: shown[i].Label, Rate: rates, Ceiling: a.Thresholds.Ceiling()}
		sa.SevenDay, sa.SevenDayReset = simWindow(rd.SevenDay)
		sa.FiveHour, sa.FiveHourReset = simWindow(rd.FiveHour)
		for _, l := range rd.WeeklyScoped {
			sa.Scoped = append(sa.Scoped, claudeusage.SimScoped{Model: l.Model, Percent: l.Percent, Reset: l.ResetsAt})
		}
		sim = append(sim, sa)
		if v.OldestReadingAt == nil || rd.FetchedAt.Before(*v.OldestReadingAt) {
			t := rd.FetchedAt
			v.OldestReadingAt = &t
		}
	}
	res := claudeusage.Simulate(sim, now)
	v.State = string(res.State)
	labelAt := func(label string, at *time.Time) *pigtail.UsageLabelAt {
		if at == nil {
			return nil
		}
		return &pigtail.UsageLabelAt{Label: label, At: *at}
	}
	switch res.State {
	case claudeusage.VerdictOK, claudeusage.VerdictTight:
		v.NextRenewal = labelAt(res.NextRenewalLabel, res.NextRenewalAt)
	case claudeusage.VerdictLater:
		v.BlockedFrom = res.BlockedFrom
		v.Frees = labelAt(res.FreesLabel, res.FreesAt)
	case claudeusage.VerdictNow:
		v.Frees = labelAt(res.FreesLabel, res.FreesAt)
	}
	return v
}

// simWindow is a window's usage now (0 once its reset has passed) and reset.
func simWindow(w *claudeusage.WindowView) (float64, *time.Time) {
	if w == nil {
		return 0, nil
	}
	if w.ResetPassed {
		return 0, w.ResetsAt
	}
	return w.RawPercent, w.ResetsAt
}

// usageAccount is one account as the route shows it.
func usageAccount(a claudeusage.Account, now time.Time) pigtail.UsageAccount {
	ua := pigtail.UsageAccount{AccountUUID: a.AccountUUID, Label: a.Label, Scoped: []pigtail.UsageScoped{}}
	if a.LabelFromCells { // Build joined cell ids: never shown (D19)
		ua.Label = "other-" + shortUUID(a.AccountUUID)
	}
	rd := a.Reading
	switch {
	case rd == nil || rd.SevenDay == nil:
		ua.Unusable = "no-reading"
	case rd.CapReached:
		ua.Unusable = "cap"
	}
	ua.InVerdict = a.Usable && ua.Unusable == ""
	if rd == nil {
		return ua
	}
	t := rd.FetchedAt
	ua.FetchedAt = &t
	ua.SevenDay = usageWindow(rd.SevenDay, a.Thresholds.Ceiling())
	ua.FiveHour = usageWindow(rd.FiveHour, a.Thresholds.Ceiling())
	for _, l := range rd.WeeklyScoped {
		w := pigtail.UsageWindow{Percent: int(math.Round(l.Percent)), ResetsAt: l.ResetsAt}
		passed := l.ResetsAt != nil && !l.ResetsAt.After(now)
		if passed {
			w.Percent = 0
		}
		w.Full = !passed && l.Percent >= a.Thresholds.Ceiling()
		ua.Scoped = append(ua.Scoped, pigtail.UsageScoped{Model: l.Model, UsageWindow: w})
	}
	if v := rd.SevenDay; v != nil && v.ResetsAt != nil {
		reset := *v.ResetsAt
		for !reset.After(now) { // a passed reset: a fresh window, as Percent is 0
			reset = reset.Add(usageWeek)
		}
		left := min(max(float64(reset.Sub(now))/float64(usageWeek), 0), 1)
		ua.WindowLeft = &left
		avail := float64(100 - ua.SevenDay.Percent)
		ua.Fast = ua.Unusable == "" && !ua.SevenDay.Full && avail < left*100-claudeusage.FastPoints
	}
	return ua
}

func usageWindow(v *claudeusage.WindowView, margin float64) *pigtail.UsageWindow {
	if v == nil {
		return nil
	}
	return &pigtail.UsageWindow{Percent: v.Percent, ResetsAt: v.ResetsAt, Full: !v.ResetPassed && v.RawPercent >= margin}
}

// refreshFailed reads the sweep's last state (usage-refresh.json): the
// accounts whose last background refresh failed. Absent or unreadable: none.
func (u *usageOverviewSource) refreshFailed() map[string]bool {
	out := map[string]bool{}
	if u.stateDir == "" {
		return out
	}
	b, err := os.ReadFile(filepath.Join(u.stateDir, claudeusage.BackgroundStateName))
	if err != nil {
		return out
	}
	var st claudeusage.BackgroundState
	if json.Unmarshal(b, &st) != nil {
		return out
	}
	for _, r := range st.Results {
		if r.Status == claudeusage.RefreshFailed {
			out[r.AccountUUID] = true
		}
	}
	return out
}

// shortUUID is an account uuid's first 8 characters (claudeusage's short).
func shortUUID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

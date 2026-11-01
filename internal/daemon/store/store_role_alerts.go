package store

import (
	"sort"
	"time"

	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Role alerts are deterministic journal rows (role supervision): kind
// role-alert (id role-silent:<name>:<ms>), closed by a role-recovered row of
// the same key or acknowledged by an alert-resolved row.
const (
	RoleAlertKind     = "role-alert"
	RoleRecoveredKind = "role-recovered"
	roleAckPrefix     = "alert-resolved:"
)

// RoleAlertRow is one role alert line with its parsed id.
type RoleAlertRow struct {
	pigtail.JournalEntry
	Name  string
	Key   time.Time
	Acked bool
}

// pagedJournal reads every row of a filter, page by page.
func (s *Store) pagedJournal(f JournalFilter) ([]pigtail.JournalEntry, error) {
	f.Limit = pigtail.JournalListMax
	var out []pigtail.JournalEntry
	seen := map[string]bool{} // a row written while paging shifts the pages by one
	for off := 0; ; off += pigtail.JournalListMax {
		f.Offset = off
		es, err := s.ListJournal(f)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if k := e.Scope + "|" + e.ID; !seen[k] {
				seen[k] = true
				out = append(out, e)
			}
		}
		if len(es) < pigtail.JournalListMax {
			return out, nil
		}
	}
}

// UnrecoveredRoleAlerts lists the role alerts with no role-recovered row of
// the same key in their scope, acked or not, oldest first.
func (s *Store) UnrecoveredRoleAlerts(filter scope.Scope) ([]RoleAlertRow, error) {
	alerts, err := s.pagedJournal(JournalFilter{Scope: filter, Kind: RoleAlertKind})
	if err != nil {
		return nil, err
	}
	if len(alerts) == 0 {
		return nil, nil
	}
	rec, err := s.pagedJournal(JournalFilter{Scope: filter, Kind: RoleRecoveredKind})
	if err != nil {
		return nil, err
	}
	ack, err := s.pagedJournal(JournalFilter{Scope: filter, Kind: "alert-resolved", IDPrefix: roleAckPrefix + "role-silent:"})
	if err != nil {
		return nil, err
	}
	recovered := map[string]bool{}
	for _, e := range rec {
		if name, key, ok := roles.ParseRecoveredID(e.ID); ok {
			recovered[e.Scope+"|"+roles.AlertID(name, key)] = true
		}
	}
	acked := map[string]bool{}
	for _, e := range ack {
		acked[e.Scope+"|"+e.ID[len(roleAckPrefix):]] = true
	}
	var out []RoleAlertRow
	for _, e := range alerts {
		name, key, ok := roles.ParseAlertID(e.ID)
		if !ok || recovered[e.Scope+"|"+e.ID] {
			continue
		}
		out = append(out, RoleAlertRow{JournalEntry: e, Name: name, Key: key, Acked: acked[e.Scope+"|"+e.ID]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// OpenRoleAlerts is UnrecoveredRoleAlerts without the acked ones.
func (s *Store) OpenRoleAlerts(filter scope.Scope) ([]RoleAlertRow, error) {
	all, err := s.UnrecoveredRoleAlerts(filter)
	if err != nil {
		return nil, err
	}
	var out []RoleAlertRow
	for _, r := range all {
		if !r.Acked {
			out = append(out, r)
		}
	}
	return out, nil
}

// RecoveredRoleKeys lists the keys of the role-recovered rows of one role.
func (s *Store) RecoveredRoleKeys(sc scope.Scope, name string) ([]time.Time, error) {
	rec, err := s.pagedJournal(JournalFilter{Scope: sc, Kind: RoleRecoveredKind, IDPrefix: "role-recovered:" + name + ":"})
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, e := range rec {
		if n, key, ok := roles.ParseRecoveredID(e.ID); ok && n == name {
			out = append(out, key)
		}
	}
	return out, nil
}

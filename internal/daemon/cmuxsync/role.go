package cmuxsync

import (
	"fmt"
	"sort"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// The « Role » pill (role supervision): a role bound to this workspace's
// subject missed its deadline. A key of its own, above the attention pill's
// neighbours but under it (90).
const (
	rolePriority = 87
	roleIcon     = "exclamationmark.triangle.fill"
	roleColor    = "#FF9F0A" // systemOrange
)

// RoleAlert is one open, unrecovered, unacknowledged role alert.
type RoleAlert struct {
	SubjectID string
	Role      string
	State     string // silent | stuck | absent
	Since     time.Time
}

// RolePill is what a workspace shows for its subject's oldest open role alert.
type RolePill struct {
	Role  string
	State string
	Since time.Time
	Now   time.Time
}

// roleAge: under an hour in multiples of 5 minutes, else whole hours.
func roleAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute)/5*5)
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

func roleStatus(p RolePill) cmux.StatusEntry {
	v := fmt.Sprintf("Role %s · %s · %s", p.State, p.Role, roleAge(p.Now.Sub(p.Since)))
	return cmux.StatusEntry{Key: KeyRole, Value: v, Icon: roleIcon, Color: roleColor, Priority: rolePriority}
}

// roleBySubject keeps each subject's oldest alert (ties: by role name).
func roleBySubject(as []RoleAlert) map[string]RoleAlert {
	sorted := append([]RoleAlert(nil), as...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].Since.Equal(sorted[j].Since) {
			return sorted[i].Since.Before(sorted[j].Since)
		}
		return sorted[i].Role < sorted[j].Role
	})
	out := map[string]RoleAlert{}
	for _, a := range sorted {
		if _, ok := out[a.SubjectID]; !ok && a.SubjectID != "" {
			out[a.SubjectID] = a
		}
	}
	return out
}

// roleAlertSet reads Deps.RoleAlerts for this pass. A read error keeps the
// last good list, logged once per distinct error. nil Deps = none.
func (r *Runner) roleAlertSet() []RoleAlert {
	if r.deps.RoleAlerts == nil {
		return nil
	}
	as, err := r.deps.RoleAlerts()
	if err != nil {
		if msg := err.Error(); msg != r.lastRoleErr {
			r.lastRoleErr = msg
			r.deps.Logf("cmuxsync: list role alerts: %v", err)
		}
	} else {
		r.lastRoleErr = ""
		r.lastRoles = as
	}
	return r.lastRoles
}

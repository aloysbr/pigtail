package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail role` declares the roles pigtaild supervises (role supervision,
// sub_x000000000000016): a subject that must act on a cadence. One alert per
// missed deadline, shown by `pigtail alerts` and a sidebar pill.

func roleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "Declare and read the roles pigtaild supervises (a subject that must act on a cadence)",
	}
	cmd.AddCommand(roleAddCmd(), roleLsCmd(), roleRmCmd(), roleStatusCmd())
	return cmd
}

func roleAddCmd() *cobra.Command {
	var subject string
	var cadence, grace, stall time.Duration
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "add <name>",
		Annotations: scopedWrite(),
		Short:       "Declare (or update) a role bound to a subject",
		Long: `Declare a role: the subject must act at least every --cadence. The role follows
the subject (a resumed session, a new tab, a fresh session in the same tab),
not one session.
pigtaild raises one alert per missed deadline after --grace (default 10m), and
one when a busy turn writes nothing for --stall (default 20m; raise it for a
role whose tool calls write nothing for longer). The role's scope is its
subject's. Standby is not an excuse: a parked role alerts once.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cadence <= 0 {
				return fmt.Errorf("--cadence is required (e.g. 30m)")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r, err := c.UpsertRole(cx, pigtail.RoleRequest{Name: args[0], SubjectID: subject,
				CadenceMS: cadence.Milliseconds(), GraceMS: grace.Milliseconds(), StallMS: stall.Milliseconds()})
			if err != nil {
				return err
			}
			fmt.Printf("✓ role %s ← %s (every %s, grace %s, stall %s, %s)\n", r.Name, r.SubjectID, r.Cadence, r.Grace, r.Stall, r.Scope)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&subject, "subject", "", "the pigtail subject id the role is bound to (required)")
	f.DurationVar(&cadence, "cadence", 0, "how often the subject must act, e.g. 30m (required)")
	f.DurationVar(&grace, "grace", 0, "wait past the deadline before alerting (default 10m)")
	f.DurationVar(&stall, "stall", 0, "a busy turn writing nothing for this long is stuck (default 20m)")
	addCrossScopeFlag(cmd, &crossScope)
	_ = cmd.MarkFlagRequired("subject")
	return cmd
}

func roleLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "ls",
		Annotations: scopedRead(),
		Short:       "List the declared roles",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rs, err := c.Roles(cx)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(pigtail.RolesResponse{Roles: nonNilRoles(rs)})
			}
			if len(rs) == 0 {
				fmt.Println("no role declared")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			for _, r := range rs {
				fmt.Fprintf(tw, "%s\t%s\t%s\tevery %s\tgrace %s\tstall %s\n", r.Name, r.Scope, r.SubjectID, r.Cadence, r.Grace, r.Stall)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func nonNilRoles(rs []pigtail.Role) []pigtail.Role {
	if rs == nil {
		return []pigtail.Role{}
	}
	return rs
}

func roleRmCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "rm <name>",
		Annotations: scopedWrite(),
		Short:       "Remove a role and close its open alerts",
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.DeleteRole(cx, args[0]); err != nil {
				return err
			}
			fmt.Printf("✓ role %s removed\n", args[0])
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func roleStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "status [name]",
		Annotations: scopedRead(),
		Short:       "Show each role's last verdict (ok, silent, stuck, absent, unknown) and why",
		Args:        cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			st, err := c.RolesStatus(cx)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				var keep []pigtail.RoleStatus
				for _, r := range st.Roles {
					if r.Role.Name == args[0] {
						keep = append(keep, r)
					}
				}
				if len(keep) == 0 {
					return fmt.Errorf("no role named %s", args[0])
				}
				st.Roles = keep
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(st)
			}
			if !st.Watch {
				fmt.Println("watch: false — pigtaild runs with -role-watch=false, no role is judged")
			}
			if len(st.Roles) == 0 {
				fmt.Println("no role declared")
				return nil
			}
			now := time.Now()
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			for _, r := range st.Roles {
				fmt.Fprintf(tw, "%s\t%s\t%s\t— %s\n", r.Role.Name, r.State, roleDue(r, now), r.Reason)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

// roleDue renders « due 11:31 + 10m (41m) »: the deadline, the grace, and how
// long past it (or « in 5m » before it).
func roleDue(r pigtail.RoleStatus, now time.Time) string {
	if r.Deadline == nil {
		return "-"
	}
	d := *r.Deadline
	age := strings.TrimSuffix(now.Sub(d).Round(time.Minute).String(), "0s")
	if now.Before(d) {
		age = "in " + strings.TrimSuffix(d.Sub(now).Round(time.Minute).String(), "0s")
	}
	return fmt.Sprintf("due %s + %s (%s)", d.Local().Format("15:04"), r.Role.Grace, age)
}

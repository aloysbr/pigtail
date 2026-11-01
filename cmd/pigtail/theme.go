package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func themeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme",
		Short: "Manage themes: a theme is a project (kind ongoing = a continuous project) that groups subjects, linked to a vault folder",
	}
	cmd.AddCommand(themeListCmd(), themeCreateCmd(), themeSetCmd(), themeDeleteCmd(), themeSpacesCmd())
	return cmd
}

func themeListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: scopedRead(),
		Short:       "List themes",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			themes, err := c.Themes(cx, scopeOverride())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(themes)
			}
			for _, th := range themes {
				fmt.Println(themeLine(th))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

// themeLine is one `theme list` row; a theme with a space adds `  {<space>}`, one
// with a deadline then adds `  due <deadline>`.
func themeLine(th pigtail.Theme) string {
	line := fmt.Sprintf("%s  [%s]  %s  %s  → %s", th.ID, th.Kind, th.Scope, th.Name, th.VaultLink)
	if th.Space != "" {
		line += "  {" + th.Space + "}"
	}
	if th.Deadline != "" {
		line += "  due " + th.Deadline
	}
	return line
}

func themeSpacesCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "spaces",
		Annotations: scopedRead(),
		Short:       "List the spaces (cmux sidebar folders) this cell may name, in folder order",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			spaces, err := c.Spaces(cx)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(spaces)
			}
			for _, sp := range spaces {
				fmt.Printf("%s  (%s)\n", sp.Name, sp.Scope)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func themeCreateCmd() *cobra.Command {
	var name, kind, vaultLink, codeProject, space string
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "create --name <name> --kind project|ongoing --vault-link <path>",
		Annotations: scopedWrite(),
		Short:       "Create a theme",
		RunE: func(_ *cobra.Command, _ []string) error {
			if name == "" || kind == "" || vaultLink == "" {
				return fmt.Errorf("--name, --kind and --vault-link are required")
			}
			nk, ok := pigtail.NormalizeThemeKind(kind)
			if !ok {
				return fmt.Errorf(`--kind must be "project" or "ongoing"`)
			}
			kind = nk
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			th, err := c.CreateTheme(cx, pigtail.Theme{
				Name: name, Kind: kind, VaultLink: vaultLink, CodeProjectID: codeProject, Space: space,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(th)
			}
			fmt.Printf("✓ created %s  [%s]  %s  %s → %s\n", th.ID, th.Kind, th.Scope, th.Name, th.VaultLink)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "theme name")
	cmd.Flags().StringVar(&kind, "kind", "", "project|ongoing (a project, or a continuous project that never closes)")
	cmd.Flags().StringVar(&vaultLink, "vault-link", "", "vault folder, e.g. Globex/Projects/rain-gauge — its vault root (scopes.yaml) sets the theme's scope")
	cmd.Flags().StringVar(&space, "space", "", "space (sidebar folder) of the theme's sessions — see `pigtail theme spaces`")
	cmd.Flags().StringVar(&codeProject, "code-project", "", "default pigtail project id (optional)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// themeUpdateFields collects the mutable fields parsed from `key=value` args.
// Pointers distinguish "not provided" from an explicit empty value.
type themeUpdateFields struct {
	name      *string
	vaultLink *string
	space     *string
	deadline  *string
	carried   *bool
}

// applyThemeField records one key=value pair onto fields. Only name,
// vault_link, space, deadline and carried are settable; id/kind/created_at are immutable.
func applyThemeField(fields *themeUpdateFields, key, val string) error {
	switch key {
	case "name":
		v := val
		fields.name = &v
	case "vault_link":
		v := val
		fields.vaultLink = &v
	case "space":
		v := val
		fields.space = &v
	case "deadline":
		if err := pigtail.ValidThemeDeadline(val); err != nil {
			return err
		}
		v := val
		fields.deadline = &v
	case "carried":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("carried must be true or false (got %q)", val)
		}
		fields.carried = &b
	default:
		return fmt.Errorf("unknown field %q (settable: name, vault_link, space, deadline, carried)", key)
	}
	return nil
}

var errOlderThemeDaemon = fmt.Errorf("the daemon did not apply deadline/carried: pigtaild is older than this pigtail (make install, then restart pigtaild)")

func themeSetCmd() *cobra.Command {
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "set <theme_id> key=value...",
		Annotations: scopedWrite(),
		Short:       `Set fields on a theme (name, vault_link, space, deadline=YYYY-MM-DD|"", carried=true|false). DB only — does not move vault files.`,
		Args:        cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			var fields themeUpdateFields
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("expected key=value, got %q", kv)
				}
				if err := applyThemeField(&fields, k, v); err != nil {
					return err
				}
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			th, err := c.UpdateTheme(cx, args[0], pigtail.ThemeUpdate{
				Name: fields.name, VaultLink: fields.vaultLink, Space: fields.space,
				Deadline: fields.deadline, Carried: fields.carried,
			})
			newFields := fields.deadline != nil || fields.carried != nil
			if err != nil {
				// A pre-0034 daemon sees no field it knows when only the new keys are sent.
				if newFields && fields.name == nil && fields.vaultLink == nil && fields.space == nil &&
					strings.Contains(err.Error(), "nothing to update") {
					return errOlderThemeDaemon
				}
				return err
			}
			// An older daemon drops deadline/carried silently and answers 200:
			// compare the answer with what was asked (before the --json branch).
			// A clear sent with name/vault_link/space against it stays undetected.
			if (fields.deadline != nil && th.Deadline != *fields.deadline) ||
				(fields.carried != nil && th.Carried != *fields.carried) {
				return errOlderThemeDaemon
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(th)
			}
			fmt.Printf("✓ updated %s  [%s]  %s → %s\n", th.ID, th.Kind, th.Name, th.VaultLink)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

func themeDeleteCmd() *cobra.Command {
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "delete <theme_id>",
		Annotations: scopedWrite(),
		Short:       "Delete a theme (refused if subjects are still linked). DB only — does not touch vault files.",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			if err := c.DeleteTheme(cx, args[0]); err != nil {
				return err
			}
			fmt.Printf("✓ deleted %s\n", args[0])
			return nil
		},
	}
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

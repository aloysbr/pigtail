package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/skills"
)

func skillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install or print the default Claude Code skill pigtail ships",
		Long: `pigtail ships one Claude Code skill, "pigtail": it teaches an agent to read its
subject, register what it produces and declare what it waits for. The same file
is readable in the repository under skills/pigtail/SKILL.md.`,
	}
	cmd.AddCommand(skillsInstallCmd(), skillsShowCmd())
	return cmd
}

func skillsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the pigtail skill",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, err := c.OutOrStdout().Write(skills.Pigtail)
			return err
		},
	}
}

// skillTarget is ${CLAUDE_CONFIG_DIR:-~/.claude}/skills/pigtail/SKILL.md.
func skillTarget() (string, error) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "skills", "pigtail", "SKILL.md"), nil
}

func skillsInstallCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the pigtail skill to $CLAUDE_CONFIG_DIR (default ~/.claude)/skills/pigtail/SKILL.md",
		Long: `Writes the skill when absent, says "already up to date" when the installed file is
identical, and refuses (exit 1) to overwrite a different one unless --force.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			path, err := skillTarget()
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			cur, err := os.ReadFile(path)
			switch {
			case err == nil && bytes.Equal(cur, skills.Pigtail):
				_, _ = fmt.Fprintf(out, "already up to date: %s\n", path)
				return nil
			case err == nil && !force:
				return fmt.Errorf("%s differs from the skill this pigtail ships; --force overwrites it", path)
			case err != nil && !errors.Is(err, os.ErrNotExist):
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(path, skills.Pigtail, 0o644); err != nil { //nolint:gosec // a skill is meant to be readable
				return err
			}
			_, _ = fmt.Fprintf(out, "installed: %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite a different installed skill")
	return cmd
}

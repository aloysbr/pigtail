package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const zshrcMarker = "# pigtail completions"

func InstallZshCompletions(configDir string, rootCmd *cobra.Command) error {
	compDir := filepath.Join(configDir, "completions")
	if err := os.MkdirAll(compDir, 0o750); err != nil {
		return err
	}
	compFile := filepath.Join(compDir, "_pigtail")

	if rootCmd != nil {
		f, err := os.Create(compFile)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		return rootCmd.GenZshCompletion(f)
	}

	return os.WriteFile(compFile, []byte("#compdef pigtail\n# run: pigtail completion zsh > this_file\n"), 0o644)
}

func ZshrcSnippet(configDir string) string {
	return fmt.Sprintf(`%s
fpath=(%s/completions $fpath)
autoload -Uz compinit && compinit
alias pt=pigtail
compdef pt=pigtail`, zshrcMarker, configDir)
}

func ZshrcNeedsUpdate(zshrcPath, configDir string) (bool, error) {
	content, err := os.ReadFile(zshrcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return !strings.Contains(string(content), zshrcMarker), nil
}

func AppendToZshrc(zshrcPath, configDir string) error {
	f, err := os.OpenFile(zshrcPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "\n%s\n", ZshrcSnippet(configDir))
	return err
}

func RemoveFromZshrc(zshrcPath string) error {
	content, err := os.ReadFile(zshrcPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	var kept []string
	skip := false
	for _, line := range lines {
		if strings.Contains(line, zshrcMarker) {
			skip = true
			continue
		}
		if skip && (strings.HasPrefix(line, "fpath=") ||
			strings.HasPrefix(line, "autoload -Uz compinit") ||
			strings.HasPrefix(line, "alias pt=") ||
			strings.HasPrefix(line, "compdef pt=")) {
			continue
		}
		skip = false
		kept = append(kept, line)
	}
	return os.WriteFile(zshrcPath, []byte(strings.Join(kept, "\n")), 0o600) //nolint:gosec // zshrcPath is always ~/.zshrc
}

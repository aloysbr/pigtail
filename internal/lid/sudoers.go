package lid

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The sudoers rule of `pigtail lid setup`: the user may run exactly
// `pmset -a disablesleep 0` and `pmset -a disablesleep 1` as root without a
// password — no wildcard, no other argument. Everything here is pure: the
// root script runs in cmd/pigtail (lid_setup.go), never in pigtaild.

var (
	userRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	aliasRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`) // what sudoers reads as a User_Alias
)

const sudoersTmp = SudoersPath + ".tmp" // a name with a "." is skipped by #includedir

func checkUser(user string) error {
	switch {
	case !userRE.MatchString(user):
		return fmt.Errorf("user name %q is not a plain user name", user)
	case user == "ALL", user == "Defaults":
		return fmt.Errorf("user name %q is a sudoers reserved word", user)
	case aliasRE.MatchString(user):
		return fmt.Errorf("user name %q would read as a sudoers User_Alias", user)
	}
	return nil
}

// SudoersRule is the content of SudoersPath for user, "\n"-terminated.
func SudoersRule(user string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	return "# Managed by pigtail lid setup. Remove with: pigtail lid setup --remove\n" +
		user + " ALL=(root) NOPASSWD: /usr/bin/pmset -a disablesleep 0, /usr/bin/pmset -a disablesleep 1\n", nil
}

// InstallScript is the one-line root shell script that writes the rule
// itself (no user-writable input, no variable path), validates it with
// visudo -cf, moves it into place and checks the whole sudoers with visudo -c,
// removing it on any failure.
func InstallScript(user string) (string, error) {
	rule, err := SudoersRule(user)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(rule, "\n"), "\n")
	quoted := make([]string, len(lines))
	for i, l := range lines {
		quoted[i] = "'" + l + "'" // a validated name carries no quote, backslash or space
	}
	return "/bin/mkdir -p -m 0755 /etc/sudoers.d" +
		" && /usr/bin/printf '%s\\n' " + strings.Join(quoted, " ") + " > " + sudoersTmp +
		" && /usr/sbin/chown root:wheel " + sudoersTmp +
		" && /bin/chmod 0440 " + sudoersTmp +
		" && { /usr/sbin/visudo -cf " + sudoersTmp + " >/dev/null || { /bin/rm -f " + sudoersTmp + "; exit 1; }; }" +
		" && /bin/mv -f " + sudoersTmp + " " + SudoersPath +
		" && { /usr/sbin/visudo -c >/dev/null || { /bin/rm -f " + SudoersPath + "; exit 1; }; }", nil
}

// RemoveScript removes the rule and any temp file a failed install left.
func RemoveScript() string {
	return "/bin/rm -f " + SudoersPath + " " + sudoersTmp
}

// AppleScript wraps a shell script for the macOS admin dialog. It refuses a
// double quote and doubles every backslash (the only escape needed).
func AppleScript(shell string) (string, error) {
	if strings.Contains(shell, `"`) {
		return "", errors.New("applescript: the script must not contain a double quote")
	}
	return `do shell script "` + strings.ReplaceAll(shell, `\`, `\\`) + `" with administrator privileges`, nil
}

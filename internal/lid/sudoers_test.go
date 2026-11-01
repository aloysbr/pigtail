package lid

import (
	"strings"
	"testing"
)

const ruleAda = "# Managed by pigtail lid setup. Remove with: pigtail lid setup --remove\n" +
	"ada ALL=(root) NOPASSWD: /usr/bin/pmset -a disablesleep 0, /usr/bin/pmset -a disablesleep 1\n"

func TestSudoersRuleExact(t *testing.T) {
	got, err := SudoersRule("ada")
	if err != nil {
		t.Fatal(err)
	}
	if got != ruleAda {
		t.Fatalf("rule:\n%q\nwant:\n%q", got, ruleAda)
	}
	if strings.ContainsAny(got, "*\"'\\") {
		t.Fatal("rule must carry no wildcard, no quote, no backslash")
	}
	for _, bad := range []string{"", "a b", "root;x", "x\nALL", "-x", "ALL", "a'b", `a\b`, "ALIAS", "A1_B", "Defaults"} {
		if _, err := SudoersRule(bad); err == nil {
			t.Errorf("SudoersRule(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"all", "Ada"} {
		if _, err := SudoersRule(ok); err != nil {
			t.Errorf("SudoersRule(%q) refused: only ALL, an all-caps User_Alias shape and Defaults are reserved, like sudoers: %v", ok, err)
		}
	}
}

func TestInstallScript(t *testing.T) {
	s, err := InstallScript("ada")
	if err != nil {
		t.Fatal(err)
	}
	want := "/bin/mkdir -p -m 0755 /etc/sudoers.d" +
		" && /usr/bin/printf '%s\\n' '# Managed by pigtail lid setup. Remove with: pigtail lid setup --remove'" +
		" 'ada ALL=(root) NOPASSWD: /usr/bin/pmset -a disablesleep 0, /usr/bin/pmset -a disablesleep 1'" +
		" > /etc/sudoers.d/pigtail-lid.tmp" +
		" && /usr/sbin/chown root:wheel /etc/sudoers.d/pigtail-lid.tmp" +
		" && /bin/chmod 0440 /etc/sudoers.d/pigtail-lid.tmp" +
		" && { /usr/sbin/visudo -cf /etc/sudoers.d/pigtail-lid.tmp >/dev/null || { /bin/rm -f /etc/sudoers.d/pigtail-lid.tmp; exit 1; }; }" +
		" && /bin/mv -f /etc/sudoers.d/pigtail-lid.tmp /etc/sudoers.d/pigtail-lid" +
		" && { /usr/sbin/visudo -c >/dev/null || { /bin/rm -f /etc/sudoers.d/pigtail-lid; exit 1; }; }"
	if s != want {
		t.Fatalf("script:\n%s\nwant:\n%s", s, want)
	}
	// The bytes printf writes are exactly the rule: each line, single-quoted, in order.
	for _, line := range strings.Split(strings.TrimSuffix(ruleAda, "\n"), "\n") {
		if !strings.Contains(s, "'"+line+"'") {
			t.Errorf("script does not write rule line %q", line)
		}
	}
	// No input root could read from a place the user can write.
	for _, ref := range []string{"/var/", "/tmp", "/private/var", "/Users", "$", "`", "~", "install "} {
		if strings.Contains(s, ref) {
			t.Errorf("root script references %q", ref)
		}
	}
	for _, bad := range []string{"ALL", "a b", "a'b", "x\nALL", ""} {
		if _, err := InstallScript(bad); err == nil {
			t.Errorf("InstallScript(%q) accepted", bad)
		}
	}
}

func TestAppleScript(t *testing.T) {
	got, err := AppleScript("/bin/rm -f /etc/sudoers.d/pigtail-lid")
	if err != nil || got != `do shell script "/bin/rm -f /etc/sudoers.d/pigtail-lid" with administrator privileges` {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := AppleScript(`printf '%s\n' x`); err != nil || got != `do shell script "printf '%s\\n' x" with administrator privileges` {
		t.Fatalf("backslash not doubled: %q, %v", got, err)
	}
	if _, err := AppleScript(`a"b`); err == nil {
		t.Error(`AppleScript("a\"b") accepted`)
	}
}

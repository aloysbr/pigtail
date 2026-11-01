package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -cmux-folders (a bool, off by default in pigtaild) switches the folder
// arrangement of the cmux sidebar on. A plist
// carrying it must survive `pigtail setup`, never abort it as an unknown flag.

func TestReadPlistTuning_cmuxFolders(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want bool
	}{
		{"--cmux-folders", true},
		{"--cmux-folders=false", false},
		{"-cmux-folders=1", true},
	} {
		got, err := ReadPlistTuning(writePlistArgs(t, tc.arg, "--max-servers", "8"))
		if err != nil {
			t.Fatalf("%s: %v", tc.arg, err)
		}
		if got.CmuxFolders == nil || *got.CmuxFolders != tc.want {
			t.Errorf("%s = %v, want %v", tc.arg, got.CmuxFolders, tc.want)
		}
		if got.MaxServers == nil || *got.MaxServers != 8 {
			t.Errorf("%s swallowed the next flag", tc.arg)
		}
	}
}

func TestGeneratePlistCmuxFolders(t *testing.T) {
	plist, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "cmux-folders") {
		t.Fatalf("unset: the flag must be omitted so pigtaild's default applies:\n%s", plist)
	}
	on := true
	first, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", CmuxFolders: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "<string>--cmux-folders</string>") {
		t.Fatalf("on must be emitted:\n%s", first)
	}
	off := false
	if p, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", CmuxFolders: &off}); err != nil || !strings.Contains(p, "<string>--cmux-folders=false</string>") {
		t.Fatalf("an explicit off must be emitted (%v):\n%s", err, p)
	}
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	params, err := PreserveTuning(path, PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePlist(params)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("round trip changed the plist:\n--- first\n%s\n--- second\n%s", first, second)
	}
}

package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -cmux-folder-sort (a bool, on by default in pigtaild
//): a plist carrying it survives `pigtail setup`, never aborts it.

func TestReadPlistTuning_cmuxFolderSort(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want bool
	}{
		{"--cmux-folder-sort", true},
		{"--cmux-folder-sort=false", false},
		{"-cmux-folder-sort=0", false},
	} {
		got, err := ReadPlistTuning(writePlistArgs(t, tc.arg, "--max-servers", "8"))
		if err != nil {
			t.Fatalf("%s: %v", tc.arg, err)
		}
		if got.CmuxFolderSort == nil || *got.CmuxFolderSort != tc.want {
			t.Errorf("%s = %v, want %v", tc.arg, got.CmuxFolderSort, tc.want)
		}
		if got.MaxServers == nil || *got.MaxServers != 8 {
			t.Errorf("%s swallowed the next flag", tc.arg)
		}
	}
}

func TestGeneratePlistCmuxFolderSort(t *testing.T) {
	plist, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "cmux-folder-sort") {
		t.Fatalf("unset: the flag must be omitted so pigtaild's default applies:\n%s", plist)
	}
	off := false
	first, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", CmuxFolderSort: &off})
	if err != nil || !strings.Contains(first, "<string>--cmux-folder-sort=false</string>") {
		t.Fatalf("an explicit off must be emitted (%v):\n%s", err, first)
	}
	on := true
	if p, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", CmuxFolderSort: &on}); err != nil || !strings.Contains(p, "<string>--cmux-folder-sort</string>") {
		t.Fatalf("on must be emitted (%v):\n%s", err, p)
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

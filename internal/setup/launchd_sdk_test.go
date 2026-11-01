package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -sdk-host / -sdk-node name paths of one machine and -sdk-launch is an opt-in:
// no deployment default, unset omits the three, a set value survives
// `pigtail setup`, and a plist carrying them must not make setup abort.
func TestSDKFlagsAreCarried(t *testing.T) {
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	plain, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"sdk-host", "sdk-node", "sdk-launch"} {
		if strings.Contains(plain, f) {
			t.Fatalf("unset must omit --%s:\n%s", f, plain)
		}
	}
	host := base
	host.SDKHost, host.SDKNode = "/h/sdkhost", "/n/node"
	got, err := GeneratePlist(host)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>--sdk-host</string>\n    <string>/h/sdkhost</string>", "<string>--sdk-node</string>\n    <string>/n/node</string>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sdk-launch") {
		t.Fatalf("--sdk-launch must stay out unless asked:\n%s", got)
	}
	host.SDKLaunch = true
	got, err = GeneratePlist(host)
	if err != nil || !strings.Contains(got, "<string>--sdk-launch</string>") {
		t.Fatalf("SDKLaunch must emit a bare --sdk-launch (%v):\n%s", err, got)
	}

	// Read back, every spelling.
	tun, err := ReadPlistTuning(writePlistArgs(t, "--sdk-host", "/h/sdkhost", "-sdk-node=/n/node", "--sdk-launch", "--max-servers", "8"))
	if err != nil {
		t.Fatalf("a plist with the sdk flags was refused: %v", err)
	}
	if tun.SDKHost != "/h/sdkhost" || tun.SDKNode != "/n/node" || !tun.SDKLaunch || tun.MaxServers == nil || *tun.MaxServers != 8 {
		t.Fatalf("got %+v", tun)
	}
	if tun, err = ReadPlistTuning(writePlistArgs(t, "--sdk-launch=false")); err != nil || tun.SDKLaunch {
		t.Fatalf("--sdk-launch=false: %+v %v", tun, err)
	}

	// Carried forward by setup.
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
		t.Fatal(err)
	}
	params, err := PreserveTuning(path, base)
	if err != nil || params.SDKHost != "/h/sdkhost" || params.SDKNode != "/n/node" || !params.SDKLaunch {
		t.Fatalf("setup dropped the sdk flags: %+v %v", params, err)
	}
}

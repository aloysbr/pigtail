package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The session launcher (sub_x000000000000015) is documented: its contract in
// wire-protocol.md, the daemon's third cmux place in architecture.md (four since the
// dead-input probe, five since « Aller à la session »), and § 7
// promises no Idempotency-Key the daemon never read.
func TestDocsSessionLaunch(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(f string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			t.Fatalf("%s read empty", f)
		}
		return string(b)
	}
	claude := read("docs/architecture.md")
	// The dead-input probe (sub_x000000000000024) is the fourth place, the
	// reveal of POST /peers/{session}/reveal (sub_x000000000000025) the fifth.
	if !strings.Contains(claude, "five places") || strings.Contains(claude, "four places") || strings.Contains(claude, "three places") || strings.Contains(claude, "exactly two places") {
		t.Error("architecture.md must say pigtaild executes cmux in five places, no more « four », « three » nor « exactly two places »")
	}
	if !strings.Contains(claude, "TestRevealOnlyInRevealHandler") {
		t.Error("architecture.md § cmux sidebar must name TestRevealOnlyInRevealHandler")
	}
	wire := read("docs/wire-protocol.md")
	for _, s := range []string{"POST /subjects/{id}/launch", "GET /launches/{id}", string(pigtail.EventSessionLaunchChanged), "launch_dir", "POST /peers/{session}/reveal"} {
		if !strings.Contains(wire, s) {
			t.Errorf("wire-protocol.md lacks %q", s)
		}
	}
	for _, c := range []pigtail.ErrorCode{pigtail.ErrSessionLive, pigtail.ErrLaunchInProgress, pigtail.ErrLaunchBurst,
		pigtail.ErrNoCell, pigtail.ErrBypassRefused, pigtail.ErrCellModeUnreadable, pigtail.ErrNoLaunchDir,
		pigtail.ErrCmuxUnreachable, pigtail.ErrCmuxCreateFailed, pigtail.ErrLiveUnknown, pigtail.ErrRegistryUnreadable} {
		if !strings.Contains(wire, string(c)) {
			t.Errorf("wire-protocol.md lacks the error code %s", c)
		}
	}
	_, idem, ok := strings.Cut(wire, "## 7. Idempotency")
	if !ok {
		t.Fatal("wire-protocol.md has no § 7 Idempotency")
	}
	idem, _, _ = strings.Cut(idem, "\n## ")
	if strings.Contains(idem, "accept an `Idempotency-Key`") || strings.Contains(idem, "Repeated calls with the same key") {
		t.Errorf("§ 7 still promises an Idempotency-Key header: %s", idem)
	}
}

// The web front's writes are documented (web front web actions, lot 1): the
// write allowlist with its Origin rule, the rank route, the web front-only
// refusal; no more « read-only » cookie in § Web (browser) nor § 5; tab_title in
// § Session launch.
func TestWebWritesDocumented(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "wire-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	wire := string(b)
	section := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(wire, start)
		if !ok {
			t.Fatalf("wire-protocol.md has no %q", start)
		}
		body, _, _ := strings.Cut(rest, end)
		return body
	}
	webSec := section("### Web (browser)", "\n### ")
	auth := section("## 5. Authentication & security", "\n## 6")
	launch := section("#### Session launch", "\n#### ")
	for _, s := range []string{"POST /subjects/ranks", string(pigtail.ErrRankChanged), string(pigtail.ErrForbiddenForWeb),
		"`Origin` required", "GET /captures", "GET /launches/{id}", "POST /subjects/{id}/launch", "DELETE /captures/{id}",
		"`X-Pigtail-Cross-Scope: write`"} {
		if !strings.Contains(webSec, s) {
			t.Errorf("§ Web (browser) lacks %q", s)
		}
	}
	for name, sec := range map[string]string{"§ Web (browser)": webSec, "§ 5 Authentication": auth} {
		if strings.Contains(sec, "any write all answer 401") || strings.Contains(strings.ToLower(sec), "read-only") {
			t.Errorf("%s still calls the cookie read-only", name)
		}
	}
	if !strings.Contains(launch, `"tab_title"`) {
		t.Error("§ Session launch lacks tab_title in its SessionLaunch example")
	}
}

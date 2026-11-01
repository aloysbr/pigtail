package launch

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestLauncherListReadsEveryOrigin(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	h.appendEntry(spawnreg.Entry{TS: now, Kind: spawnreg.KindLaunchReserve, ID: "ln_cli", SubjectID: "sub_x", Origin: "cli", OwnerPID: os.Getpid(), Result: spawnreg.ResultPending})
	h.appendEntry(spawnreg.Entry{TS: now.Add(-time.Minute), Kind: spawnreg.KindLaunchReserve, ID: "ln_api", SubjectID: "sub_y", Origin: Origin, OwnerPID: os.Getpid(), Result: spawnreg.ResultPending})
	h.appendEntry(spawnreg.Entry{TS: now.Add(-30 * time.Second), Kind: spawnreg.KindLaunchResult, ID: "ln_api", SubjectID: "sub_y", Origin: Origin, Result: spawnreg.ResultFailed, Detail: "boom"})

	got, err := h.launcher().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %+v, want two launches", got)
	}
	byID := map[string]pigtail.SessionLaunch{}
	for _, s := range got {
		byID[s.LaunchID] = s
	}
	if c := byID["ln_cli"]; c.State != pigtail.LaunchPending || c.Origin != "cli" || c.SubjectID != "sub_x" {
		t.Fatalf("cli launch = %+v, want pending cli on sub_x", c)
	}
	if a := byID["ln_api"]; a.State != pigtail.LaunchFailed || a.Origin != Origin || a.SubjectID != "sub_y" {
		t.Fatalf("api launch = %+v, want failed api on sub_y", a)
	}
}

func TestLauncherBurstCountsRecent(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		h.appendEntry(spawnreg.Entry{TS: now.Add(-time.Duration(i) * time.Second), Kind: spawnreg.KindLaunchReserve,
			ID: "ln_" + strconv.Itoa(i), SubjectID: "sub_" + strconv.Itoa(i), Origin: Origin, OwnerPID: os.Getpid(), Result: spawnreg.ResultPending})
	}
	b, err := h.launcher().Burst()
	if err != nil {
		t.Fatal(err)
	}
	if !b.Full() || b.Recent != 10 {
		t.Fatalf("Burst = %+v, want full with 10 recent", b)
	}
}

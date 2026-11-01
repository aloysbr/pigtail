package rammon

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	const floor = uint64(6 << 30) // 6 GiB
	grace := 10 * time.Minute
	idle := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_idle", Server: "dev", Port: 3030, MemBytes: 5 << 30, IdleFor: 20 * time.Minute}
	idle2 := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_idle2", Server: "dev", Port: 3040, MemBytes: 2 << 30, IdleFor: 30 * time.Minute}
	active := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_active", Server: "dev", Port: 3050, MemBytes: 5 << 30, IdleFor: 1 * time.Minute}

	if d := decide([]ServerStat{idle}, floor+1, floor, grace, grace, false); d.Action != ActionNone {
		t.Fatalf("above floor: %v", d.Action)
	}
	if d := decide([]ServerStat{active}, floor-1, floor, grace, grace, false); d.Action != ActionWarn {
		t.Fatalf("all active: %v", d.Action)
	}
	d := decide([]ServerStat{idle, idle2, active}, floor-1, floor, grace, grace, false)
	if d.Action != ActionEvict || d.Victim == nil || d.Victim.WorkspaceID != "ws_idle2" {
		t.Fatalf("evict victim: %+v", d)
	}
	if d := decide(nil, floor-1, floor, grace, grace, false); d.Action != ActionWarn {
		t.Fatalf("empty: %v", d.Action)
	}
}

func TestDecideBoundaries(t *testing.T) {
	const floor = uint64(6 << 30)
	grace := 10 * time.Minute
	// available == floor → None (>= floor is headroom)
	s := ServerStat{IdleConfirmed: true, WorkspaceID: "x", IdleFor: 30 * time.Minute, MemBytes: 1 << 30}
	if d := decide([]ServerStat{s}, floor, floor, grace, grace, false); d.Action != ActionNone {
		t.Fatalf("available==floor should be None, got %v", d.Action)
	}
	// IdleFor == grace exactly (below floor) → that server IS a candidate → Evict
	atGrace := ServerStat{IdleConfirmed: true, WorkspaceID: "atgrace", IdleFor: grace, MemBytes: 1 << 30}
	if d := decide([]ServerStat{atGrace}, floor-1, floor, grace, grace, false); d.Action != ActionEvict || d.Victim.WorkspaceID != "atgrace" {
		t.Fatalf("IdleFor==grace should be evictable, got %+v", d)
	}
}

func TestDecideTieBreakMem(t *testing.T) {
	const floor = uint64(6 << 30)
	grace := 10 * time.Minute
	a := ServerStat{IdleConfirmed: true, WorkspaceID: "a", IdleFor: 20 * time.Minute, MemBytes: 1 << 30}
	b := ServerStat{IdleConfirmed: true, WorkspaceID: "b", IdleFor: 20 * time.Minute, MemBytes: 4 << 30}
	d := decide([]ServerStat{a, b}, floor-1, floor, grace, grace, false)
	if d.Action != ActionEvict || d.Victim.WorkspaceID != "b" {
		t.Fatalf("tie-break by memory should pick b: %+v", d)
	}
}

// TestDecideMainsUnchanged pins the existing mains behaviour byte-for-byte:
// onBattery=false must produce exactly the same Action/Victim as before the
// battery param existed (batteryGrace is irrelevant on this path).
func TestDecideMainsUnchanged(t *testing.T) {
	const floor = uint64(6 << 30)
	grace := 10 * time.Minute
	batteryGrace := 10 * time.Minute
	idle := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_idle", Server: "dev", Port: 3030, MemBytes: 5 << 30, IdleFor: 20 * time.Minute}
	idle2 := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_idle2", Server: "dev", Port: 3040, MemBytes: 2 << 30, IdleFor: 30 * time.Minute}
	active := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_active", Server: "dev", Port: 3050, MemBytes: 5 << 30, IdleFor: 1 * time.Minute}

	// above floor → None
	if d := decide([]ServerStat{idle}, floor+1, floor, grace, batteryGrace, false); d.Action != ActionNone {
		t.Fatalf("mains above floor: %v", d.Action)
	}
	// below floor, all active → Warn
	if d := decide([]ServerStat{active}, floor-1, floor, grace, batteryGrace, false); d.Action != ActionWarn {
		t.Fatalf("mains all active: %v", d.Action)
	}
	// below floor, most idle wins
	d := decide([]ServerStat{idle, idle2, active}, floor-1, floor, grace, batteryGrace, false)
	if d.Action != ActionEvict || d.Victim == nil || d.Victim.WorkspaceID != "ws_idle2" {
		t.Fatalf("mains evict victim: %+v", d)
	}
}

// TestDecideBattery covers the new battery branch: floor is ignored.
func TestDecideBattery(t *testing.T) {
	const floor = uint64(6 << 30)
	grace := 10 * time.Minute
	batteryGrace := 10 * time.Minute
	idle := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_idle", MemBytes: 1 << 30, IdleFor: 20 * time.Minute}
	idle2 := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_idle2", MemBytes: 2 << 30, IdleFor: 30 * time.Minute}
	active := ServerStat{IdleConfirmed: true, WorkspaceID: "ws_active", MemBytes: 5 << 30, IdleFor: 1 * time.Minute}

	// Plenty of RAM (above floor) but on battery → still evict the most idle.
	d := decide([]ServerStat{idle, idle2, active}, floor+1, floor, grace, batteryGrace, true)
	if d.Action != ActionEvict || d.Victim == nil || d.Victim.WorkspaceID != "ws_idle2" {
		t.Fatalf("battery evict most-idle ignoring floor: %+v", d)
	}
	// No server idle enough → None (no warn spam), even with low RAM.
	if d := decide([]ServerStat{active}, floor-1, floor, grace, batteryGrace, true); d.Action != ActionNone {
		t.Fatalf("battery none-idle should be None, got %v", d.Action)
	}
	// Tie on idle → largest RSS wins.
	a := ServerStat{IdleConfirmed: true, WorkspaceID: "a", IdleFor: 20 * time.Minute, MemBytes: 1 << 30}
	b := ServerStat{IdleConfirmed: true, WorkspaceID: "b", IdleFor: 20 * time.Minute, MemBytes: 4 << 30}
	if d := decide([]ServerStat{a, b}, floor+1, floor, grace, batteryGrace, true); d.Action != ActionEvict || d.Victim.WorkspaceID != "b" {
		t.Fatalf("battery tie-break by memory: %+v", d)
	}
	// batteryGrace is the threshold on battery, independent of mains grace:
	// idle 12m with batteryGrace 10m is evictable even if mains grace were 15m.
	s := ServerStat{IdleConfirmed: true, WorkspaceID: "s", IdleFor: 12 * time.Minute, MemBytes: 1 << 30}
	if d := decide([]ServerStat{s}, floor+1, floor, 15*time.Minute, 10*time.Minute, true); d.Action != ActionEvict {
		t.Fatalf("battery uses batteryGrace not grace: %+v", d)
	}
}

func TestIdlePurgeVictims(t *testing.T) {
	servers := []ServerStat{
		{IdleConfirmed: true, WorkspaceID: "ws1", Server: "dev", IdleFor: 30 * time.Minute},
		{IdleConfirmed: true, WorkspaceID: "ws2", Server: "dev", IdleFor: 1 * time.Minute},
		{IdleConfirmed: true, WorkspaceID: "ws3", Server: "dev", IdleFor: 10 * time.Minute}, // == grace boundary
	}

	// grace = 0 → disabled, no victims.
	if got := idlePurgeVictims(servers, 0); len(got) != 0 {
		t.Fatalf("grace=0: got %d victims, want 0", len(got))
	}

	// grace = 10m → ws1 (30m) and ws3 (==10m), not ws2 (1m).
	got := idlePurgeVictims(servers, 10*time.Minute)
	if len(got) != 2 {
		t.Fatalf("grace=10m: got %d victims, want 2: %+v", len(got), got)
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.WorkspaceID] = true
	}
	if !ids["ws1"] || !ids["ws3"] || ids["ws2"] {
		t.Fatalf("grace=10m: wrong victims %v", ids)
	}
}

func TestOverCapVictims(t *testing.T) {
	servers := []ServerStat{
		{IdleConfirmed: true, WorkspaceID: "a", IdleFor: 30 * time.Minute, MemBytes: 100},
		{IdleConfirmed: true, WorkspaceID: "b", IdleFor: 10 * time.Minute, MemBytes: 100},
		{IdleConfirmed: true, WorkspaceID: "c", IdleFor: 1 * time.Minute, MemBytes: 100},  // below minIdle
		{IdleConfirmed: true, WorkspaceID: "d", IdleFor: 10 * time.Minute, MemBytes: 200}, // tie with b, larger RSS
	}
	minIdle := 5 * time.Minute

	// n <= 0 disables.
	if got := overCapVictims(servers, 0, minIdle); got != nil {
		t.Fatalf("n=0: got %+v, want nil", got)
	}

	// n=1 → the single most-idle eligible server (a, 30m).
	got := overCapVictims(servers, 1, minIdle)
	if len(got) != 1 || got[0].WorkspaceID != "a" {
		t.Fatalf("n=1: got %v, want [a]", ids(got))
	}

	// n=2 → a (30m) then the 10m tie broken by RSS: d (200) before b (100).
	got = overCapVictims(servers, 2, minIdle)
	if len(got) != 2 || got[0].WorkspaceID != "a" || got[1].WorkspaceID != "d" {
		t.Fatalf("n=2: got %v, want [a d]", ids(got))
	}

	// n=10 but only 3 eligible (a, b, d; c is below minIdle) → returns 3, never c.
	got = overCapVictims(servers, 10, minIdle)
	if len(got) != 3 {
		t.Fatalf("n=10: got %d victims, want 3", len(got))
	}
	for _, s := range got {
		if s.WorkspaceID == "c" {
			t.Fatalf("evicted server c which is below minIdle")
		}
	}
}

// ids is a small test helper for readable failure messages.
func ids(ss []ServerStat) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.WorkspaceID
	}
	return out
}

// --- IdleConfirmed: an idle reading restored from disk cannot evict ---

// A server whose idle time was restored from the previous daemon's state file
// has a plausible idle reading but no observation made by *this* daemon. The
// silent-death design (2026-07-27, §2) traded idle continuity for the guarantee
// that a restart can never trigger a wrongful eviction; persisting the state
// must not buy the first back by giving up the second. So the restored value is
// good enough to report, never good enough to kill on.
func TestUnconfirmedIdleIsNeverEvicted(t *testing.T) {
	stale := ServerStat{
		WorkspaceID: "ws", Server: "dev", Port: 3000,
		IdleFor: 2 * time.Hour, MemBytes: 900 << 20, IdleConfirmed: false,
	}

	t.Run("decide does not pick it under RAM pressure", func(t *testing.T) {
		got := decide([]ServerStat{stale}, 1<<30, 6<<30, time.Minute, time.Minute, false)
		if got.Action == ActionEvict {
			t.Fatalf("decide evicted an unconfirmed server: victim=%+v", got.Victim)
		}
		if got.Action != ActionWarn {
			t.Fatalf("decide action = %v, want Warn (nothing evictable)", got.Action)
		}
	})

	t.Run("idle purge skips it", func(t *testing.T) {
		if v := idlePurgeVictims([]ServerStat{stale}, time.Minute); len(v) != 0 {
			t.Fatalf("idlePurgeVictims = %+v, want none", v)
		}
	})

	t.Run("over-cap eviction skips it", func(t *testing.T) {
		if v := overCapVictims([]ServerStat{stale}, 1, time.Minute); len(v) != 0 {
			t.Fatalf("overCapVictims = %+v, want none", v)
		}
	})
}

// The guard must not become a blanket amnesty: once this daemon has observed
// the server once, the same idle reading evicts exactly as before.
func TestConfirmedIdleStillEvicts(t *testing.T) {
	fresh := ServerStat{
		WorkspaceID: "ws", Server: "dev", Port: 3000,
		IdleFor: 2 * time.Hour, MemBytes: 900 << 20, IdleConfirmed: true,
	}
	if got := decide([]ServerStat{fresh}, 1<<30, 6<<30, time.Minute, time.Minute, false); got.Action != ActionEvict {
		t.Fatalf("decide action = %v, want Evict", got.Action)
	}
	if v := idlePurgeVictims([]ServerStat{fresh}, time.Minute); len(v) != 1 {
		t.Fatalf("idlePurgeVictims = %+v, want 1", v)
	}
	if v := overCapVictims([]ServerStat{fresh}, 1, time.Minute); len(v) != 1 {
		t.Fatalf("overCapVictims = %+v, want 1", v)
	}
}

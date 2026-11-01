package websession

import (
	"encoding/base64"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestCodeSingleUse(t *testing.T) {
	c := NewCodes(CodeTTL, time.Now)
	code, err := c.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Redeem(code) {
		t.Fatal("first Redeem of a fresh code failed")
	}
	if c.Redeem(code) {
		t.Fatal("second Redeem succeeded: a code must be single-use")
	}
}

func TestCodeExpires(t *testing.T) {
	if CodeTTL != 2*time.Minute {
		t.Fatalf("CodeTTL = %v, want 2m", CodeTTL)
	}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := NewCodes(CodeTTL, clk.now)
	fresh, _ := c.Issue()
	stale, _ := c.Issue()
	clk.add(CodeTTL - time.Second)
	if !c.Redeem(fresh) {
		t.Fatal("a code redeemed within its TTL must succeed (positive control)")
	}
	clk.add(2 * time.Second)
	if c.Redeem(stale) {
		t.Fatal("a code redeemed after its TTL succeeded")
	}
}

func TestCodeRandomness(t *testing.T) {
	c := NewCodes(CodeTTL, time.Now)
	a, _ := c.Issue()
	b, _ := c.Issue()
	if a == b {
		t.Fatal("two issued codes are equal")
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		t.Fatalf("code %q is not raw URL base64: %v", a, err)
	}
	if len(raw)*8 < 128 {
		t.Fatalf("code carries %d bits, want >= 128", len(raw)*8)
	}
}

func TestCodeUnknownAndEmptyRejected(t *testing.T) {
	c := NewCodes(CodeTTL, time.Now)
	if _, err := c.Issue(); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"", "nope", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if c.Redeem(v) {
			t.Fatalf("Redeem(%q) succeeded", v)
		}
	}
}

func TestCodesPendingCap(t *testing.T) {
	c := NewCodes(CodeTTL, time.Now)
	first, _ := c.Issue()
	var last string
	for i := 0; i < MaxPending; i++ {
		last, _ = c.Issue()
	}
	if got := c.pendingCount(); got > MaxPending {
		t.Fatalf("%d codes pending, cap is %d", got, MaxPending)
	}
	if c.Redeem(first) {
		t.Fatal("the oldest code must be evicted once the cap is passed")
	}
	if !c.Redeem(last) {
		t.Fatal("the newest code must survive the cap (positive control)")
	}
}

func TestCodesConcurrent(t *testing.T) {
	c := NewCodes(CodeTTL, time.Now)
	code, _ := c.Issue()
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Redeem(code) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
			_, _ = c.Issue()
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent Redeem calls won, want exactly 1", wins)
	}
}

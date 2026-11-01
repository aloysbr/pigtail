package netprobe

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestReachableDialsBounded(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := Reachable(context.Background(), addr, time.Second); err != nil {
		t.Fatalf("listening port: %v", err)
	}
	_ = l.Close()
	start := time.Now()
	if err := Reachable(context.Background(), addr, time.Second); err == nil {
		t.Fatal("closed port reported reachable")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("closed port took %v, want < timeout+1s", d)
	}
}

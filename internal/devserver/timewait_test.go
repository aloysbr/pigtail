package devserver

import (
	"errors"
	"net"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestParseNetstatTimeWait(t *testing.T) {
	out := []byte(`Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  127.0.0.1.5173         127.0.0.1.50001        TIME_WAIT
tcp4       0      0  127.0.0.1.50002        127.0.0.1.6006         TIME_WAIT
tcp6       0      0  ::1.5173               ::1.50003              TIME_WAIT
tcp4       0      0  10.0.0.2.50004         34.1.2.3.443           TIME_WAIT
tcp4       0      0  127.0.0.1.5173         127.0.0.1.50005        ESTABLISHED
tcp4       0      0  *.5173                 *.*                    LISTEN
`)
	got := parseNetstatTimeWait(out)
	if len(got[5173]) != 2 {
		t.Fatalf("5173 = %v, want the IPv4 and IPv6 server-side TIME_WAITs", got[5173])
	}
	if len(got[6006]) != 1 {
		t.Fatalf("6006 = %v, want the loopback client-side TIME_WAIT attributed to the server port", got[6006])
	}
	if _, ok := got[443]; ok {
		t.Fatal("a non-loopback remote port must not be attributed")
	}
	if len(got[50002]) != 1 {
		t.Fatalf("the client side keeps its own local port too: %v", got[50002])
	}
}

func TestParseSSTimeWait(t *testing.T) {
	out := []byte("0 0 127.0.0.1:5173 127.0.0.1:50001\n0 0 [::1]:5173 [::1]:50003\n0 0 127.0.0.1:50002 127.0.0.1:6006\n")
	got := parseSSTimeWait(out)
	if len(got[5173]) != 2 || len(got[6006]) != 1 {
		t.Fatalf("got %v", got)
	}
}

// The same connection seen at two sweeps must yield the same key, so the
// tracker's set difference counts it once.
func TestTimeWaitKeysAreStable(t *testing.T) {
	line := []byte("tcp4 0 0 127.0.0.1.5173 127.0.0.1.50001 TIME_WAIT\n")
	a, b := parseNetstatTimeWait(line), parseNetstatTimeWait(line)
	if a[5173][0] != b[5173][0] {
		t.Fatalf("unstable key: %q vs %q", a[5173][0], b[5173][0])
	}
}

func TestListTimeWaitSeesAServerClose(t *testing.T) {
	tool := map[string]string{"darwin": "netstat", "linux": "ss"}[runtime.GOOS]
	if tool == "" {
		t.Skip("no TIME_WAIT inventory on " + runtime.GOOS)
	}
	if _, err := exec.LookPath(tool); err != nil {
		t.Skip(tool + " not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	srv.Close() // the server closes first, as on a keep-alive timeout
	buf := make([]byte, 1)
	_, _ = client.Read(buf) // EOF
	client.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tw, err := ListTimeWait()
		if errors.Is(err, ErrTCPTableHidden) {
			// This test's own listener and TIME_WAIT socket make pcbcount > 0,
			// so under an ad-hoc-signed ancestor the hidden branch is
			// deterministic. The real TIME_WAIT path is not proven here.
			t.Skipf("%v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(tw[port]) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no TIME_WAIT seen on :%d after a server-side close", port)
}

const netstatHeader = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
`

func TestParsePCBCount(t *testing.T) {
	if n, err := parsePCBCount([]byte("273\n")); err != nil || n != 273 {
		t.Fatalf("got %d, %v; want 273, nil", n, err)
	}
	for _, in := range []string{"", "-1", "abc"} {
		if _, err := parsePCBCount([]byte(in)); err == nil {
			t.Fatalf("parsePCBCount(%q) should fail", in)
		}
	}
}

func TestNetstatVisible(t *testing.T) {
	listen := netstatHeader + "tcp4       0      0  *.5173                 *.*                    LISTEN\n"
	cases := []struct {
		name  string
		out   string
		count int
		want  bool
	}{
		{"listen row, no TIME_WAIT", listen, 5, true},
		{"header only, pcbs exist", netstatHeader, 5, false},
		{"empty, no pcbs", "", 0, true},
	}
	for _, c := range cases {
		if got := netstatVisible([]byte(c.out), c.count); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestListTimeWaitNamesAHiddenTable(t *testing.T) {
	listen := netstatHeader + "tcp4       0      0  *.5173                 *.*                    LISTEN\n"
	tw := netstatHeader + "tcp4       0      0  127.0.0.1.5173         127.0.0.1.50001        TIME_WAIT\n"
	counter := func(n int, err error) (func() (int, error), *int) {
		calls := 0
		return func() (int, error) { calls++; return n, err }, &calls
	}

	t.Run("empty output, pcbs exist", func(t *testing.T) {
		f, _ := counter(42, nil)
		if _, err := timeWaitFromNetstat([]byte(""), f); !errors.Is(err, ErrTCPTableHidden) {
			t.Fatalf("err = %v, want ErrTCPTableHidden", err)
		}
	})
	t.Run("header only, pcbs exist", func(t *testing.T) {
		f, _ := counter(42, nil)
		if _, err := timeWaitFromNetstat([]byte(netstatHeader), f); !errors.Is(err, ErrTCPTableHidden) {
			t.Fatalf("err = %v, want ErrTCPTableHidden", err)
		}
	})
	t.Run("header only, no pcbs", func(t *testing.T) {
		f, _ := counter(0, nil)
		got, err := timeWaitFromNetstat([]byte(netstatHeader), f)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v; want empty, nil", got, err)
		}
	})
	t.Run("count error is not hidden", func(t *testing.T) {
		f, _ := counter(0, errors.New("boom"))
		_, err := timeWaitFromNetstat([]byte(netstatHeader), f)
		if err == nil || errors.Is(err, ErrTCPTableHidden) {
			t.Fatalf("err = %v, want a non-hidden error", err)
		}
	})
	t.Run("listen row, count never called", func(t *testing.T) {
		f, calls := counter(42, nil)
		got, err := timeWaitFromNetstat([]byte(listen), f)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v; want empty, nil", got, err)
		}
		if *calls != 0 {
			t.Fatalf("count called %d times, want 0", *calls)
		}
	})
	t.Run("time_wait row, count never called", func(t *testing.T) {
		f, calls := counter(42, nil)
		got, err := timeWaitFromNetstat([]byte(tw), f)
		if err != nil || len(got[5173]) != 1 {
			t.Fatalf("got %v, %v", got, err)
		}
		if *calls != 0 {
			t.Fatalf("count called %d times, want 0", *calls)
		}
	})
}

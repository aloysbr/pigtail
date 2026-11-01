package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// getCPUSlots drives the real router, so this covers the route registration
// as well as the config mapping.
func getCPUSlots(t *testing.T, s *Server) pigtail.CPUSlotsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/cpu/slots", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /cpu/slots = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data pigtail.CPUSlotsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

func TestCPUSlotsConfigExplicitBudget(t *testing.T) {
	cfg := Config{SocketPath: testSocketPath(t), HTTPAddr: "", CPUSlots: 3}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := getCPUSlots(t, s).Budget; got != 3 {
		t.Fatalf("budget = %d, want 3", got)
	}
}

// Zero is the daemon-side kill switch, and it is also the zero value: a
// Config that never mentions cpu slots leaves the arbiter off.
func TestCPUSlotsConfigZeroDisables(t *testing.T) {
	cfg := Config{SocketPath: testSocketPath(t), HTTPAddr: ""}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := getCPUSlots(t, s).Budget; got != 0 {
		t.Fatalf("budget = %d, want 0 (disabled)", got)
	}
	if s.cpuSlots.Enabled() {
		t.Fatalf("arbiter reports enabled at budget 0")
	}
}

// A negative budget asks for the platform default — what pigtaild's flag
// passes when the user does not override it.
func TestCPUSlotsConfigNegativeMeansPlatformDefault(t *testing.T) {
	cfg := Config{SocketPath: testSocketPath(t), HTTPAddr: "", CPUSlots: -1}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := getCPUSlots(t, s).Budget; got < 1 {
		t.Fatalf("budget = %d, want the platform default (>= 1)", got)
	}
}

func TestCPUSlotMaxHoldMapping(t *testing.T) {
	cfg := Config{
		SocketPath:     testSocketPath(t),
		HTTPAddr:       "",
		CPUSlots:       2,
		CPUSlotMaxHold: 90 * time.Second,
	}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.cpuSlotMaxHold(); got != 90*time.Second {
		t.Fatalf("max hold = %s, want 1m30s", got)
	}
}

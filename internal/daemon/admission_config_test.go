package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// getAdmission drives the real router, covering route registration as well
// as the config mapping.
func getAdmission(t *testing.T, s *Server) pigtail.AdmissionStatus {
	t.Helper()
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/admission", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admission = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data pigtail.AdmissionStatus `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

// Zero is the kill switch and the zero value: a Config that never mentions
// admission leaves it off, exactly like CPU slots.
func TestAdmissionConfigZeroDisables(t *testing.T) {
	s, err := New(Config{SocketPath: testSocketPath(t)}, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := getAdmission(t, s).MaxJobs; got != 0 {
		t.Fatalf("max_jobs = %d, want 0 (disabled)", got)
	}
	if s.admission.Enabled() {
		t.Fatal("controller reports enabled at max_jobs 0")
	}
}

func TestAdmissionConfigMapping(t *testing.T) {
	cfg := Config{
		SocketPath:           testSocketPath(t),
		AdmitMaxJobs:         3,
		AdmitFloorMB:         8192,
		AdmitPressureLevel:   4,
		AdmitSwapoutKBPerSec: 1024,
		AdmitMaxHold:         5 * time.Minute,
	}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := getAdmission(t, s)
	if st.MaxJobs != 3 || st.FloorBytes != 8192<<20 || st.PressureGate != 4 || st.SwapoutGateBytesPerSec != 1024*1024 {
		t.Fatalf("status = %+v", st)
	}
}

func TestAdmissionConfigDefaultsAndOff(t *testing.T) {
	def := admission.Defaults()
	got := admissionConfig(Config{AdmitMaxJobs: -1})
	if got.MaxJobs != def.MaxJobs || got.FloorBytes != def.FloorBytes ||
		got.PressureLevel != def.PressureLevel || got.SwapoutBytesPerSec != def.SwapoutBytesPerSec || got.MaxHold != def.MaxHold {
		t.Fatalf("-1 and zero values must give the defaults, got %+v", got)
	}
	off := admissionConfig(Config{AdmitMaxJobs: -1, AdmitPressureLevel: -1, AdmitSwapoutKBPerSec: -1})
	if off.PressureLevel != 0 || off.SwapoutBytesPerSec != 0 {
		t.Fatalf("negative values must turn the gates off, got %+v", off)
	}
}

func TestAdmissionConfigCompressGate(t *testing.T) {
	if got := admissionConfig(Config{AdmitMaxJobs: -1, AdmitCompressKBPerSec: 2048}).CompressBytesPerSec; got != 2048*1024 {
		t.Fatalf("2048 KiB/s mapped to %v", got)
	}
	if got := admissionConfig(Config{AdmitMaxJobs: -1}).CompressBytesPerSec; got != admission.Defaults().CompressBytesPerSec {
		t.Fatalf("zero must give the default, got %v", got)
	}
	if got := admissionConfig(Config{AdmitMaxJobs: -1, AdmitCompressKBPerSec: -1}).CompressBytesPerSec; got != 0 {
		t.Fatalf("negative must turn the gate off, got %v", got)
	}
}

func TestAdmissionConfigCompressCalm(t *testing.T) {
	def := admissionConfig(Config{AdmitMaxJobs: -1})
	if def.CompressCalmWindow != time.Minute || def.CompressCalmMaxStarve != 0 {
		t.Fatalf("zero values must keep the defaults, got window %v max starve %v", def.CompressCalmWindow, def.CompressCalmMaxStarve)
	}
	if got := admissionConfig(Config{AdmitMaxJobs: -1, AdmitCompressCalmWindow: 90 * time.Second}).CompressCalmWindow; got != 90*time.Second {
		t.Fatalf("window = %v", got)
	}
	if got := admissionConfig(Config{AdmitMaxJobs: -1, AdmitCompressCalmWindow: -1}).CompressCalmWindow; got != 0 {
		t.Fatalf("negative must turn the rule off, got %v", got)
	}
	if got := admissionConfig(Config{AdmitMaxJobs: -1, AdmitCompressCalmMaxStarve: 10 * time.Minute}).CompressCalmMaxStarve; got != 10*time.Minute {
		t.Fatalf("max starve = %v", got)
	}
}

// The disk floor: -1 is the default (20 GiB), 0 is off, > 0 is the value, and
// a Config that never mentions it leaves it off (like the other kill switches).
func TestAdmissionConfigDiskFloor(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want uint64
	}{{-1, 20 << 30}, {0, 0}, {35, 35 << 30}} {
		if got := admissionConfig(Config{AdmitMaxJobs: 2, AdmitDiskFloorGB: tc.in}).DiskFloorBytes; got != tc.want {
			t.Errorf("AdmitDiskFloorGB=%d: DiskFloorBytes = %d, want %d", tc.in, got, tc.want)
		}
	}
	if got := admissionConfig(Config{}).DiskFloorBytes; got != 0 {
		t.Errorf("empty Config: DiskFloorBytes = %d, want 0 (off)", got)
	}
}

// V2 (criterion 6), handler half: GET /admission carries the free disk space
// and the floor. The keys are asserted on the raw JSON so a field that goes
// missing, or gets an omitempty, fails here.
func TestAdmissionEndpointCarriesTheDisk(t *testing.T) {
	cfg := Config{SocketPath: testSocketPath(t), AdmitMaxJobs: 2, AdmitDiskFloorGB: 25}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/admission", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admission = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"disk_free_bytes", "disk_known", "disk_floor_bytes"} {
		if _, ok := env.Data[k]; !ok {
			t.Errorf("GET /admission lacks %q: %s", k, rec.Body.String())
		}
	}
	st := getAdmission(t, s)
	if st.DiskFloorBytes != 25<<30 {
		t.Errorf("disk_floor_bytes = %d, want %d", st.DiskFloorBytes, uint64(25)<<30)
	}
	if !st.DiskKnown || st.DiskFreeBytes == 0 {
		t.Errorf("the daemon reads the real disk: known=%v free=%d", st.DiskKnown, st.DiskFreeBytes)
	}
}

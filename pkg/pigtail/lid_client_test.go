package pigtail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientLid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/lid" {
			t.Errorf("got %s %s, want GET /lid", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"sleep_disabled":true,"deadline":"2026-10-16T23:00:00Z","battery_floor":15}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.Lid(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.SleepDisabled || got.Deadline == nil || got.BatteryFloor != 15 {
		t.Fatalf("unexpected lid response: %+v", got)
	}
}

func TestClientLidGuards(t *testing.T) {
	for _, c := range []struct {
		body      string
		guard     string
		thermal   string
		lidClosed *bool
	}{
		{`{"data":{"sleep_disabled":true,"battery_floor":25,"guard_error":"lid reading failed: x","thermal_state":"serious","lid_closed":false}}`,
			"lid reading failed: x", "serious", new(bool)},
		{`{"data":{"sleep_disabled":true,"battery_floor":25,"thermal_state":"fair","lid_closed":true}}`,
			"", "fair", func() *bool { b := true; return &b }()},
		{`{"data":{"sleep_disabled":true,"battery_floor":25}}`, "", "", nil},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(c.body))
		}))
		got, err := NewHTTPClient(srv.Listener.Addr().String(), "").Lid(context.Background())
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got.GuardError != c.guard || got.ThermalState != c.thermal {
			t.Errorf("%s: got %+v", c.body, got)
		}
		switch {
		case c.lidClosed == nil && got.LidClosed != nil:
			t.Errorf("%s: lid_closed absent decoded as %v", c.body, *got.LidClosed)
		case c.lidClosed != nil && (got.LidClosed == nil || *got.LidClosed != *c.lidClosed):
			t.Errorf("%s: lid_closed = %v, want %v (present false is not absent)", c.body, got.LidClosed, *c.lidClosed)
		}
	}
}

func TestClientSetLid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/lid" {
			t.Errorf("got %s %s, want POST /lid", r.Method, r.URL.Path)
		}
		var body LidRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		} else if body != (LidRequest{On: true, Hours: 3}) {
			t.Errorf("body = %+v, want on=true hours=3", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"sleep_disabled":true,"deadline":"2026-10-16T23:00:00Z","battery_floor":15}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.SetLid(context.Background(), true, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SleepDisabled || got.Deadline == nil {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestClientLidProbe(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/lid/probe" {
			t.Errorf("got %s %s, want POST /lid/probe", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"pmset -g: boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"rule_ok":true,"warning":"sleep is disabled with no timer — run pigtail lid off"}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.LidProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.RuleOK || got.Warning == "" {
		t.Fatalf("unexpected: %+v", got)
	}
	fail = true
	_, err = c.LidProbe(context.Background())
	var api *APIError
	if !errors.As(err, &api) {
		t.Fatalf("want *APIError, got %T %v", err, err)
	}
}

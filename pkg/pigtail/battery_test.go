package pigtail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientBattery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/battery" {
			t.Errorf("got %s %s, want GET /battery", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"on_battery":true,"enabled":true,"grace_seconds":600,"servers":[{"workspace_id":"ws1","server":"dev","port":3030,"idle_seconds":1800,"evict_in_seconds":0,"spared":false}]}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.Battery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.OnBattery || got.GraceSeconds != 600 || len(got.Servers) != 1 || got.Servers[0].WorkspaceID != "ws1" {
		t.Fatalf("unexpected battery response: %+v", got)
	}
}

func TestClientSetBattery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/battery" {
			t.Errorf("got %s %s, want POST /battery", r.Method, r.URL.Path)
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		} else if body.Enabled != false {
			t.Errorf("body.enabled = %v, want false", body.Enabled)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"on_battery":false,"enabled":false,"grace_seconds":600,"servers":[]}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.SetBattery(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatalf("SetBattery(false): Enabled=%v, want false", got.Enabled)
	}
}

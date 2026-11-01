package pigtail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPurge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/purge" {
			t.Errorf("got %s %s, want GET /purge", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"grace_seconds":600}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.Purge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.GraceSeconds != 600 {
		t.Fatalf("GraceSeconds=%d, want 600", got.GraceSeconds)
	}
}

func TestClientSetPurge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/purge" {
			t.Errorf("got %s %s, want POST /purge", r.Method, r.URL.Path)
		}
		var body struct {
			GraceSeconds int `json:"grace_seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		} else if body.GraceSeconds != 1800 {
			t.Errorf("body.grace_seconds = %d, want 1800", body.GraceSeconds)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"grace_seconds":1800}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")

	got, err := c.SetPurge(context.Background(), 1800)
	if err != nil {
		t.Fatal(err)
	}
	if got.GraceSeconds != 1800 {
		t.Fatalf("SetPurge: GraceSeconds=%d, want 1800", got.GraceSeconds)
	}
}

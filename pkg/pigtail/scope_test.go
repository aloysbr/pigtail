package pigtail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestClientScopeHeaders: the scope and the cross-scope opt-in travel as
// headers on every call, and only when set.
func TestClientScopeHeaders(t *testing.T) {
	var got http.Header
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotURL = r.Header.Clone(), r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	base := NewHTTPClient(srv.Listener.Addr().String(), "")
	ctx := context.Background()

	if _, err := base.Subjects(ctx, SubjectQuery{}); err != nil {
		t.Fatal(err)
	}
	if got.Get(HeaderScope) != "" || got.Get(HeaderCrossScope) != "" {
		t.Fatalf("plain client sent scope headers: %v", got)
	}

	scoped := base.WithScope("perso")
	if _, err := scoped.Subjects(ctx, SubjectQuery{Scope: "gx", Status: "inbox"}); err != nil {
		t.Fatal(err)
	}
	if got.Get(HeaderScope) != "perso" || got.Get(HeaderCrossScope) != "" {
		t.Fatalf("scoped client headers: %v", got)
	}
	if gotURL != "/subjects?scope=gx&status=inbox" {
		t.Fatalf("list URL = %s", gotURL)
	}

	cross := scoped.WithCrossScopeWrite()
	if _, err := cross.Log(ctx, "sub_1"); err != nil {
		t.Fatal(err)
	}
	if got.Get(HeaderScope) != "perso" || got.Get(HeaderCrossScope) != CrossScopeWrite {
		t.Fatalf("cross client headers: %v", got)
	}
	// With* return copies: the scoped client did not gain the opt-in.
	if _, err := scoped.Log(ctx, "sub_1"); err != nil {
		t.Fatal(err)
	}
	if got.Get(HeaderCrossScope) != "" {
		t.Fatal("WithCrossScopeWrite mutated its receiver")
	}

	if _, err := scoped.LogSince(ctx, "sub_1", time.UnixMilli(1700000000000)); err != nil {
		t.Fatal(err)
	}
	if gotURL != "/subjects/sub_1/log?since=1700000000000" {
		t.Fatalf("log URL = %s", gotURL)
	}
	if _, err := scoped.Themes(ctx, "perso"); err != nil {
		t.Fatal(err)
	}
	if gotURL != "/themes?scope=perso" {
		t.Fatalf("themes URL = %s", gotURL)
	}
}

func TestClientNextSubjectScope(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"sub_1"}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Listener.Addr().String(), "")
	if _, err := c.NextSubject(context.Background(), ""); err != nil || gotURL != "/subjects/next" {
		t.Fatalf("next = %s, %v", gotURL, err)
	}
	if _, err := c.NextSubject(context.Background(), "gx"); err != nil || gotURL != "/subjects/next?scope=gx" {
		t.Fatalf("next gx = %s, %v", gotURL, err)
	}
}

// The SSE stream is filtered by scope too: Events must send the header like
// every other call (it builds its own request).
func TestClientEventsSendsScope(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(HeaderScope)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewHTTPClient(srv.Listener.Addr().String(), "").WithScope("perso")
	if _, err := c.Events(ctx, EventsRequest{}); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h != "perso" {
		t.Fatalf("Events sent %s=%q, want perso", HeaderScope, h)
	}
}

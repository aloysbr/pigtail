package pigtail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSendsSessionHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(HeaderSession)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "").WithSession("sid-1")
	_ = c.Health(context.Background())
	if got != "sid-1" {
		t.Fatalf("header = %q", got)
	}
}

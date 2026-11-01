package pigtail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An HTTP error without pigtail's JSON envelope (chi's plain-text 404 from an
// older daemon) is an *HTTPStatusError — the daemon answered, so it is not a
// transport error — with the same text as before.
func TestClientPlainHTTPErrorIsHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "")
	_, err := c.Maintenance(context.Background())
	var st *HTTPStatusError
	if !errors.As(err, &st) || st.Status != 404 {
		t.Fatalf("want *HTTPStatusError 404, got %T %v", err, err)
	}
	if err.Error() != "pigtail: HTTP 404: 404 page not found\n" {
		t.Fatalf("message changed: %q", err.Error())
	}
}

func TestClientJSONErrorStaysAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"maintenance not wired"}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "")
	_, err := c.Maintenance(context.Background())
	var api *APIError
	var st *HTTPStatusError
	if !errors.As(err, &api) || api.Code != ErrInternal || errors.As(err, &st) {
		t.Fatalf("want *APIError INTERNAL, got %T %v", err, err)
	}
}

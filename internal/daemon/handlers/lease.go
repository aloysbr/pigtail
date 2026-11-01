package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Plumbing shared by the two lease endpoints — POST /cpu/slots and
// POST /admission/leases — where the HTTP connection itself is the lease:
// the handler answers, then blocks on the request context until the client
// goes away, and the kernel closing the socket is the release.

// decodeLeaseBody decodes the request body into v and then drains it to EOF.
// On a malformed body it answers 400 and returns false.
//
// The drain is load-bearing, not tidiness. Go's server only starts the
// background read that detects a client disconnect once the request body has
// been read to EOF (net/http Server, readLoop's "waiting for the body to be
// consumed" gate) — the whole lease rests on that read racing r.Context() as
// the client vanishes, so it must start unconditionally rather than depend on
// how much of the body json.Decoder happened to consume.
func decodeLeaseBody(w http.ResponseWriter, r *http.Request, resp Responder, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	_, _ = io.Copy(io.Discard, r.Body)
	if err != nil {
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
		return false
	}
	return true
}

// leaseFlusher returns the connection's Flusher, answering 500 and returning
// nil when streaming is unsupported.
func leaseFlusher(w http.ResponseWriter, r *http.Request, resp Responder) http.Flusher {
	f, ok := w.(http.Flusher)
	if !ok {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
			"streaming unsupported on this connection", nil)
		return nil
	}
	return f
}

// setStreamHeaders marks a response as a stream nothing may buffer. Must run
// before the first write: headers are frozen once WriteHeader runs.
func setStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
}

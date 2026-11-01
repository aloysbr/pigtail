package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// handleEvents serves GET /events as a Server-Sent Events stream.
// Supports replay via the Last-Event-ID header or `?since=evt_…`,
// and type filtering via `?types=workspace.*,agent.paused`.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
			"streaming unsupported on this connection", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Work/perso: the subscriber's scope is fixed at subscribe time (absent
	// = the default scope, like every scoped route).
	caller, err := scope.FromHeader(r.Header.Get(pigtail.HeaderScope))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
			pigtail.HeaderScope+": "+err.Error(), nil)
		return
	}

	types := parseTypes(r.URL.Query().Get("types"))
	since := r.Header.Get("Last-Event-ID")
	if since == "" {
		since = r.URL.Query().Get("since")
	}

	// Subscribe BEFORE replaying so we don't miss events that arrive
	// between replay and live tail.
	sub := s.bus.Subscribe(types...)
	defer sub.Close()

	// D29: the subscription exists — say so at once. A client that waits for
	// the first data (URLSession.bytes(for:)) would otherwise wait for the
	// first ": ping", 15 s later, when there is nothing to replay.
	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// Replay buffered events (filtered).
	for _, e := range s.bus.Replay(since, types) {
		if !eventVisible(caller, e) {
			continue
		}
		if err := writeSSE(w, e); err != nil {
			return
		}
	}
	flusher.Flush()

	// Heartbeat to detect dead clients / keep proxies alive.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			if !eventVisible(caller, e) {
				continue
			}
			if err := writeSSE(w, e); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-s.streamsStop:
			// Daemon shutting down: write what is already queued (Run
			// publishes daemon.shutting_down before Shutdown), then return so
			// net/http ends the response normally. Without the drain, select's
			// random choice would lose it half the time.
			for {
				select {
				case e, ok := <-sub.C:
					if !ok {
						return
					}
					if eventVisible(caller, e) {
						if err := writeSSE(w, e); err != nil {
							return
						}
					}
				default:
					flusher.Flush()
					return
				}
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, e pigtail.Event) error {
	payload, err := json.Marshal(map[string]interface{}{
		"id":        e.ID,
		"type":      e.Type,
		"timestamp": e.Timestamp.Format(time.RFC3339Nano),
		"data":      e.Data,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", e.ID, e.Type, payload); err != nil {
		return err
	}
	return nil
}

func parseTypes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// subjectBearingEvents are the event families whose data comes from the
// subject layer (subject ids, labels built from subject slugs). An event of
// these families must be tagged with a scope by its publisher; untagged, it
// reaches the master's subscribers only.
var subjectBearingEvents = []string{"message.", "cmux.", "wait.", "subject.", "session.launch", "staffing."}

// eventVisible applies the scope rule to one event for a subscriber.
func eventVisible(caller scope.Scope, e pigtail.Event) bool {
	if scope.SeesAll(caller) {
		return true
	}
	if e.Scope != "" {
		sc, err := scope.Parse(e.Scope)
		return err == nil && scope.CanRead(caller, sc)
	}
	for _, prefix := range subjectBearingEvents {
		if strings.HasPrefix(string(e.Type), prefix) {
			return false
		}
	}
	return true
}

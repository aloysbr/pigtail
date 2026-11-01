package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/netprobe"
)

// The auto-retry's real seams (handlers.WakeDeps, relay_retry.go).

// retryAPIReachable is the bounded TCP probe of the API.
func retryAPIReachable() bool {
	return netprobe.Reachable(context.Background(), netprobe.APIAddr, netprobe.ProbeTimeout) == nil
}

// retryTranscript finds the one transcript of the session in its config dir.
func retryTranscript(cfg, session string) (string, error) {
	ts, err := claudesession.FindAll(session, []string{cfg})
	if err != nil {
		return "", err
	}
	if len(ts) != 1 {
		return "", fmt.Errorf("%d transcripts for the session", len(ts))
	}
	return ts[0].JSONLPath(), nil
}

// retryLastAPIError is the last API error entry of the session's transcript.
func retryLastAPIError(cfg, session string) (claudesession.APIError, bool) {
	p, err := retryTranscript(cfg, session)
	if err != nil {
		return claudesession.APIError{}, false
	}
	return claudesession.LastAPIError(p)
}

// retryHumanPromptsSince are the prompts typed after since in the session's
// transcript (its tail), the relay's own "continue" included.
func retryHumanPromptsSince(cfg, session string, since time.Time) ([]claudesession.Prompt, error) {
	p, err := retryTranscript(cfg, session)
	if err != nil {
		return nil, err
	}
	ps, _, err := claudesession.PromptsAfter(p, -1, since)
	return ps, err
}

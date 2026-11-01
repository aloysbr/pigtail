package pigtail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ErrAdmissionUnresponsive is returned when the daemon accepted the
// connection but did not answer within the first-line deadline — a wedged
// daemon. Callers fail open on it, exactly as on an absent one.
var ErrAdmissionUnresponsive = errors.New("pigtail: admission did not answer in time")

// admissionFirstLineTimeout bounds how long the daemon may take to answer a
// lease request at all. The daemon writes its first line (queued or granted)
// immediately, so this only ever fires on a daemon that is stuck. A variable
// so tests need not wait two real seconds.
var admissionFirstLineTimeout = 2 * time.Second

// Admission returns the admission controller's state (GET /admission).
func (c *Client) Admission(ctx context.Context) (AdmissionStatus, error) {
	var out AdmissionStatus
	if _, err := c.do(ctx, "GET", "/admission", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// AdmissionHistory returns the commands that ended at or after since
// (GET /admission/history).
func (c *Client) AdmissionHistory(ctx context.Context, since time.Time) (AdmissionHistory, error) {
	var out AdmissionHistory
	path := "/admission/history?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// CPUSlotsHistory fetches the CPU slot holds that ended at or after since.
// A daemon older than the endpoint answers 404 (an *APIError).
func (c *Client) CPUSlotsHistory(ctx context.Context, since time.Time) (CPUSlotHistory, error) {
	var out CPUSlotHistory
	path := "/cpu/slots/history?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// AdmissionLease is a granted admission. The underlying connection *is* the
// lease: Release closes it, and so does this process dying.
type AdmissionLease struct {
	Grant AdmissionUpdate

	client *Client
	once   sync.Once
	body   io.Closer
	cancel context.CancelFunc
}

// Release gives the lease back. Safe to call more than once.
func (l *AdmissionLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = l.body.Close()
		l.cancel()
	})
}

// admissionExitTimeout bounds the exit report of Finish: the command is over,
// nothing may keep its caller waiting on the daemon.
const admissionExitTimeout = 2 * time.Second

// Finish reports the command's exit code to the daemon, then gives the lease
// back. The report is best effort and fail-open: a daemon without the route,
// a stuck or absent one only costs the history its exit_code. Safe to call
// more than once (the later calls only release).
func (l *AdmissionLease) Finish(code int) {
	if l == nil {
		return
	}
	if l.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), admissionExitTimeout)
		_, _ = l.client.do(ctx, "POST", "/admission/leases/"+url.PathEscape(l.Grant.LeaseID)+"/exit",
			AdmissionExit{ExitCode: &code}, nil)
		cancel()
	}
	l.Release()
}

// AcquireAdmission asks the daemon to admit one heavy command and blocks
// until it is granted or ctx is done. onQueued, when non-nil, receives every
// "queued" progress line.
//
// ctx bounds the *wait* only: once granted, the lease lives until Release,
// whatever ctx does afterwards.
//
// Errors worth telling apart:
//   - ctx.Err() — the caller's wait budget ran out;
//   - *APIError with ErrAdmissionDisabled — the daemon runs, admission is off;
//   - ErrAdmissionUnresponsive — the daemon did not answer at all;
//   - anything else — no daemon, or a broken stream.
//
// Every one of them means "run unadmitted" to a fail-open caller except the
// first, which is a genuine "the machine is still full".
func (c *Client) AcquireAdmission(ctx context.Context, req AdmissionRequest, onQueued func(AdmissionUpdate)) (*AdmissionLease, error) {
	buf, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// The connection must outlive ctx once granted, so it gets a context of
	// its own; ctx is watched separately and only cancels it while waiting.
	connCtx, cancel := context.WithCancel(context.Background())
	httpReq, err := http.NewRequestWithContext(connCtx, "POST", c.baseURL+"/admission/leases", bytes.NewReader(buf))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("X-Pigtail-API", APIVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	var unresponsive, waitDone bool
	var mu sync.Mutex
	firstLine := time.AfterFunc(admissionFirstLineTimeout, func() {
		mu.Lock()
		unresponsive = true
		mu.Unlock()
		cancel()
	})
	granted := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			waitDone = true
			mu.Unlock()
			cancel()
		case <-granted:
		}
	}()
	fail := func(err error) (*AdmissionLease, error) {
		firstLine.Stop()
		close(granted)
		cancel()
		mu.Lock()
		u, w := unresponsive, waitDone
		mu.Unlock()
		switch {
		case w:
			return nil, ctx.Err()
		case u:
			return nil, ErrAdmissionUnresponsive
		}
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fail(err)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var errEnv ErrorResponse
		if jsonErr := json.Unmarshal(raw, &errEnv); jsonErr == nil && errEnv.Error.Code != "" {
			return fail(&errEnv.Error)
		}
		return fail(fmt.Errorf("pigtail: HTTP %d: %s", resp.StatusCode, string(raw)))
	}

	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			_ = resp.Body.Close()
			return fail(fmt.Errorf("admission stream: %w", err))
		}
		firstLine.Stop()
		var env struct {
			Data AdmissionUpdate `json:"data"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			_ = resp.Body.Close()
			return fail(fmt.Errorf("admission stream: decode %q: %w", line, err))
		}
		switch env.Data.State {
		case AdmissionQueued:
			if onQueued != nil {
				onQueued(env.Data)
			}
		case AdmissionGranted:
			close(granted)
			lease := &AdmissionLease{Grant: env.Data, client: c, body: resp.Body, cancel: cancel}
			// A deadline that fired in the same instant as the grant: honour
			// the deadline, the caller has already given up.
			mu.Lock()
			w := waitDone
			mu.Unlock()
			if w {
				lease.Release()
				return nil, ctx.Err()
			}
			return lease, nil
		}
	}
}

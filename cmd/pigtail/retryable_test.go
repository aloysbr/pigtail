package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRetryableExitCode(t *testing.T) {
	t.Parallel()
	plain := errors.New("session respawn: boom — retry")
	if got := exitCode(plain); got != 1 {
		t.Fatalf("plain refusal exits %d, want 1", got)
	}
	r := retryable(retryShort, plain)
	if got := exitCode(r); got != exitTempFail {
		t.Fatalf("retryable exits %d, want %d", got, exitTempFail)
	}
	if got := exitCode(fmt.Errorf("wrapped: %w", r)); got != exitTempFail {
		t.Fatalf("wrapped retryable exits %d, want %d", got, exitTempFail)
	}
	if !strings.HasSuffix(r.Error(), "[retryable after 10s]") {
		t.Fatalf("text = %q", r.Error())
	}
	if retryable(retryShort, nil) != nil {
		t.Fatal("retryable(nil) must stay nil")
	}
	if got := exitCode(withExitCode(2, r)); got != 2 {
		t.Fatalf("an explicit exit code wins: %d", got)
	}
}

func TestSpawnJSONErrorRetryable(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	_ = spawnJSONError(&b, retryable(retryLong, errors.New("e")), false, true)
	var v map[string]any
	if err := json.Unmarshal(b.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["retryable"] != true || v["retry_after_s"] != float64(60) {
		t.Fatalf("retryable json = %v", v)
	}
	b.Reset()
	_ = spawnJSONError(&b, errors.New("e"), false, true)
	v = nil
	if err := json.Unmarshal(b.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["retryable"] != false {
		t.Fatalf("rule refusal json = %v", v)
	}
	if _, ok := v["retry_after_s"]; ok {
		t.Fatalf("rule refusal carries retry_after_s: %v", v)
	}
	b.Reset()
	_ = spawnJSONFailure(&b, retryable(retryArm, errors.New("e")), true, map[string]any{"workspace_ref": "workspace:1"})
	v = nil
	_ = json.Unmarshal(b.Bytes(), &v)
	if v["retryable"] != true || v["retry_after_s"] != float64(30) {
		t.Fatalf("failure json = %v", v)
	}
}

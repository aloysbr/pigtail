// Package frontapp reads which app is frontmost on macOS, without any TCC
// grant: `lsappinfo front` gives the frontmost app's ASN, `lsappinfo info
// -only bundleid <ASN>` its bundle id (measured 2026-10-20). The account
// rotation's focus guard (D11 of sub_x000000000000008) reads it to tell
// whether the user looks at cmux; every failure is an error, so the guard fails
// closed.
package frontapp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const lsappinfo = "/usr/bin/lsappinfo"

// Seams: the OS, the per-call bound and the runner (tests never run the real
// lsappinfo).
var (
	goos        = runtime.GOOS
	callTimeout = time.Second
	run         = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is the fixed lsappinfo path, args are ours
		cmd.Env = []string{}
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, err
	}
)

var (
	asnRe    = regexp.MustCompile(`ASN:0x[0-9a-fA-F]+-0x[0-9a-fA-F]+:`)
	bundleRe = regexp.MustCompile(`"CFBundleIdentifier"\s*=\s*"([^"]*)"`)
)

// BundleID is the frontmost app's bundle id. Each lsappinfo call is bounded
// by callTimeout; an empty answer, "[ NULL ]" or anything unparsed is an error.
func BundleID(ctx context.Context) (string, error) {
	if goos != "darwin" {
		return "", fmt.Errorf("front app: unsupported on %s", goos)
	}
	front, err := call(ctx, "front")
	if err != nil {
		return "", fmt.Errorf("lsappinfo front: %w", err)
	}
	asn := asnRe.FindString(front)
	if asn == "" {
		return "", fmt.Errorf("lsappinfo front: no app (%q)", oneLine(front))
	}
	info, err := call(ctx, "info", "-only", "bundleid", asn)
	if err != nil {
		return "", fmt.Errorf("lsappinfo info: %w", err)
	}
	return ParseBundleID(info)
}

// ParseBundleID reads `"CFBundleIdentifier"="<id>"`; none or empty is an error.
func ParseBundleID(out string) (string, error) {
	m := bundleRe.FindStringSubmatch(out)
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", fmt.Errorf("lsappinfo info: no bundle id (%q)", oneLine(out))
	}
	return strings.TrimSpace(m[1]), nil
}

func call(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out, err := run(ctx, lsappinfo, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("no answer in %s: %w", callTimeout, err)
		}
		return "", err
	}
	return string(out), nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

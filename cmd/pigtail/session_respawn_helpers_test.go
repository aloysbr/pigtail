package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// waitFor polls cond until it holds, failing the test after d.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func atoiFile(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// withoutVar drops NAME=… from a space-separated rendered environment.
func withoutVar(env, name string) string {
	var keep []string
	for _, f := range strings.Fields(env) {
		if !strings.HasPrefix(f, name+"=") {
			keep = append(keep, f)
		}
	}
	return strings.Join(keep, " ")
}

// withVar replaces (or adds) NAME=value in a rendered environment.
func withVar(env, name, value string) string {
	return strings.TrimSpace(withoutVar(env, name) + " " + name + "=" + value)
}

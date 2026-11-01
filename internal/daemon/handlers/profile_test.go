package handlers

import (
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestResolveProfileEnv(t *testing.T) {
	t.Parallel()
	p := &pigtail.Profile{
		ID:           "prf_a",
		ConfigDir:    "/home/u/.claude-work",
		EnvOverrides: map[string]string{"ANTHROPIC_MODEL": "claude-opus-4-8"},
	}
	env := resolveProfileEnv(p)
	if env["CLAUDE_CONFIG_DIR"] != "/home/u/.claude-work" {
		t.Fatalf("CLAUDE_CONFIG_DIR not set: %v", env)
	}
	if env["ANTHROPIC_MODEL"] != "claude-opus-4-8" {
		t.Fatalf("override not merged: %v", env)
	}
}

func TestResolveProfileEnvNilOrEmpty(t *testing.T) {
	t.Parallel()
	if env := resolveProfileEnv(nil); len(env) != 0 {
		t.Fatalf("nil profile should yield empty env, got %v", env)
	}
	if env := resolveProfileEnv(&pigtail.Profile{ID: "prf_x"}); len(env) != 0 {
		t.Fatalf("profile with no config should yield empty env, got %v", env)
	}
}

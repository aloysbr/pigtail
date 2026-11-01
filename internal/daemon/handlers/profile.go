package handlers

import "github.com/aloysbr/pigtail/pkg/pigtail"

// resolveProfileEnv builds the env overrides a spawned agent should inherit
// from its Profile: CLAUDE_CONFIG_DIR (account/config selection) plus any
// explicit env overrides. Returns an empty map for a nil/empty profile.
func resolveProfileEnv(p *pigtail.Profile) map[string]string {
	env := map[string]string{}
	if p == nil {
		return env
	}
	if p.ConfigDir != "" {
		env["CLAUDE_CONFIG_DIR"] = p.ConfigDir
	}
	for k, v := range p.EnvOverrides {
		env[k] = v
	}
	return env
}

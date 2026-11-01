// Package skills embeds the default Claude Code skills pigtail ships, so the
// binary can install them (pigtail skills install). The files stay readable on
// GitHub under skills/<name>/SKILL.md.
package skills

import _ "embed"

// Pigtail is skills/pigtail/SKILL.md.
//
//go:embed pigtail/SKILL.md
var Pigtail []byte

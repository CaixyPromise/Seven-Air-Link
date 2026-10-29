package skills

import "embed"

// Assets contains operator instructions, not model code or an Agent adapter.
//
//go:embed seven-air-link/SKILL.md
var Assets embed.FS

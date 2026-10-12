package dcgen

import "sort"

// StateDir is where a container's persisted agent configuration is mounted. It
// is outside $HOME so that no lookup of remoteUser is needed.
const StateDir = "/var/dev-state"

// stateEnv points each tool at its own directory on the state volume. These are
// the variables each tool documents for relocating its configuration. XDG_*
// would move every XDG-aware program, so it is not used.
var stateEnv = map[string]string{
	// Also moves .claude.json, so one mount covers it.
	"CLAUDE_CONFIG_DIR": StateDir + "/claude",
	"CODEX_HOME":        StateDir + "/codex",
	"HERMES_HOME":       StateDir + "/hermes",
	// Config only: opencode keeps auth.json elsewhere, so no credential is on
	// the volume.
	"OPENCODE_CONFIG_DIR": StateDir + "/opencode",
	// Not an agent, but a global git config would be lost with the container
	// filesystem.
	"GIT_CONFIG_GLOBAL": StateDir + "/gitconfig",
}

// StateEnv returns the variables pointing tools at the state volume.
func StateEnv() map[string]string {
	out := make(map[string]string, len(stateEnv))
	for k, v := range stateEnv {
		out[k] = v
	}
	return out
}

// StateEnvKeys returns those variable names, sorted.
func StateEnvKeys() []string {
	out := make([]string, 0, len(stateEnv))
	for k := range stateEnv {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package dcgen

import "sort"

// StateDir is where a container's persisted agent configuration is mounted.
//
// Outside $HOME deliberately. A project-owned configuration can name any
// remoteUser it likes, and the local provider would have to run
// read-configuration to learn which home directory to use. A fixed path needs
// no lookup and is spelled the same in the generated document, in the
// devcontainer CLI's --mount argument and in the Kubernetes volumeMount.
const StateDir = "/var/dev-state"

// stateEnv points each tool at its own directory on the state volume.
//
// Every variable here is one the tool documents for relocating its whole
// configuration directory, so nothing is symlinked and nothing is copied. A
// symlink was the alternative and is worse: `ln -sfn` against a directory that
// already exists links *inside* it rather than replacing it, silently, and the
// claude-code feature creates ~/.claude during the build.
//
// All of them are set whenever a container persists state, whatever it has
// installed. An agent that is not there never reads its variable, which is what
// keeps agents ordinary catalog tools rather than something the container has
// to declare.
//
// opencode is missing on purpose: it splits state across four XDG directories
// and its consolidating variable is documented in an issue rather than in its
// documentation. Setting XDG_* globally would relocate every XDG-aware program
// in the container, not just opencode.
var stateEnv = map[string]string{
	// Moves .claude.json as well as the directory, so one mount covers it.
	"CLAUDE_CONFIG_DIR": StateDir + "/claude",
	"CODEX_HOME":        StateDir + "/codex",
	"HERMES_HOME":       StateDir + "/hermes",
	// Not an agent, but the same problem: a `git config --global` run inside
	// the container is lost with the container filesystem otherwise. Only the
	// file moves; the host's identity still arrives as DEVCONTAINER_GIT_*.
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

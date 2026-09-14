package k8s

import (
	"fmt"
	"strings"
)

// shellQuote wraps s so a POSIX shell reads it as one literal word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoin renders an argv as a shell command line.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// envPrefix renders environment variables as an `env` invocation.
//
// The Secret already carries the workspace's settings, so this is not how they
// normally arrive. It matters for two things: a variable that belongs to one
// invocation rather than the container (HERDR_AGENT), and a secret rotated
// since the pod started — the pod's own environment is fixed at start, but a
// command exec'd into it gets the current value.
func envPrefix(env []envVar) []string {
	if len(env) == 0 {
		return nil
	}
	args := []string{"env"}
	for _, e := range env {
		args = append(args, e.Key+"="+e.Value)
	}
	return args
}

// envVar mirrors provider.EnvVar, kept local so the shell helpers in this file
// have no reason to import the provider package.
type envVar struct{ Key, Value string }

// asRemoteUser wraps a command so it runs as the configured remoteUser.
//
// kubectl exec runs as the image's own USER, and the devcontainer spec's
// remoteUser is applied by the CLI at exec time — which never happens here. The
// switch is conditional at runtime rather than decided up front because whether
// the pod is root depends on the image, which this tool does not inspect: if it
// is already the right user, or is not root and so cannot switch, the command
// runs as-is rather than failing.
func asRemoteUser(user, workdir string, argv []string) []string {
	cmd := shellJoin(argv)
	if workdir != "" {
		cmd = "cd " + shellQuote(workdir) + " && " + cmd
	}
	if user == "" {
		return []string{"sh", "-c", cmd}
	}

	script := fmt.Sprintf(
		`if [ "$(id -un)" != %s ] && [ "$(id -u)" = 0 ]; then exec su -p %s -c %s; else exec sh -c %s; fi`,
		shellQuote(user), shellQuote(user), shellQuote(cmd), shellQuote(cmd))
	return []string{"sh", "-c", script}
}

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
// normally arrive. It matters for a secret rotated since the pod started — the
// pod's own environment is fixed at start, but a command exec'd into it gets
// the current value.
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
//
// `su -p` keeps the pod's environment, which is where the Secret's settings and
// containerEnv live, but it keeps root's HOME, USER and LOGNAME with it. Those
// three are reset to the remote user's: with HOME=/root, any tool that writes
// under ~ fails with permission denied (opencode dies creating
// /root/.local/share/opencode), and the home subPath mounted at home would
// never be written at all.
//
// PATH goes the other way: su runs PAM, and pam_env replaces it with
// /etc/environment's even under -p. The devcontainer CLI rewrites that file
// with the image's PATH when it starts a container, which never happens here,
// so it still holds the distro default and everything a feature put on PATH —
// npm's global bin under nvm — vanishes, and start-agent reports an installed
// agent as missing. The pod's PATH rides across in DEV_POD_PATH and is restored
// inside the switched shell, after PAM has run; the other three are set there
// too, since pam_env may name them as well. HOME is also set before su, or the
// user's shell looks for its rc file in /root and complains it cannot read it.
func asRemoteUser(user, home, workdir string, argv []string) []string {
	cmd := shellJoin(argv)
	if workdir != "" {
		cmd = "cd " + shellQuote(workdir) + " && " + cmd
	}
	if user == "" {
		return []string{"sh", "-c", cmd}
	}

	switched := fmt.Sprintf(`export PATH="$DEV_POD_PATH" HOME=%s USER=%s LOGNAME=%s; unset DEV_POD_PATH; %s`,
		shellQuote(home), shellQuote(user), shellQuote(user), cmd)
	script := fmt.Sprintf(
		`if [ "$(id -un)" != %s ] && [ "$(id -u)" = 0 ]; then HOME=%s DEV_POD_PATH="$PATH" exec su -p %s -c %s; else exec sh -c %s; fi`,
		shellQuote(user), shellQuote(home), shellQuote(user), shellQuote(switched), shellQuote(cmd))
	return []string{"sh", "-c", script}
}

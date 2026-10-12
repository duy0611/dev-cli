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

// envPrefix renders env vars as an `env` invocation, for values exec'd into a
// pod whose own environment is fixed at start.
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

// envVar mirrors provider.EnvVar, so the shell helpers do not import the
// provider package.
type envVar struct{ Key, Value string }

// asRemoteUser wraps argv to run as the remote user. The switch is decided at
// runtime because whether the pod runs as root depends on the image, which is
// not inspected here.
//
// su -p keeps root's HOME, USER and LOGNAME, so those are reset inside the
// switched shell. HOME is also set before su, or the user's shell looks for rc
// files in /root. PAM's pam_env sets PATH from /etc/environment even under -p,
// and that file holds only the distro default here, so the pod's PATH rides in
// DEV_POD_PATH and is restored after PAM runs.
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

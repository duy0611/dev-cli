package acp

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Var is one variable for the adapter.
type Var struct{ Key, Value string }

// Quote single-quotes s for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// EnvPath is a fresh, unguessable path in the container for the env file.
func EnvPath() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "/tmp/.dev-acp-" + hex.EncodeToString(b)
}

// EnvFile renders vars as shell assignments, for the stdin of WriteCommand.
// Keys must already be valid names.
func EnvFile(vars []Var) []byte {
	var b strings.Builder
	for _, v := range vars {
		b.WriteString(v.Key + "=" + Quote(v.Value) + "\n")
	}
	return []byte(b.String())
}

// WriteCommand stores its stdin at path, readable by its user alone. noclobber
// makes the create exclusive, so a path another user planted is never followed.
func WriteCommand(path string) []string {
	return []string{"sh", "-c", `umask 077; set -C; cat > "$1"`, "sh", path}
}

// Wrapper is the argv that exports the file's variables, deletes the file and
// only then execs command, so the secrets sit on disk until the adapter starts
// and in the adapter's environment after. The file is read whole before eval,
// so it is gone even when its content is not.
func Wrapper(path string, command []string) []string {
	const script = `f=$1; shift; c=$(cat "$f") && rm -f "$f" || exit 1; set -a; eval "$c"; set +a; exec "$@"`
	return append([]string{"sh", "-c", script, "sh", path}, command...)
}

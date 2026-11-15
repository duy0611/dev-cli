// Package envfile passes environment variables into a container through a
// private file written over stdin, so neither the host's process list nor a
// cluster's exec audit log shows the values.
package envfile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/xpath"
)

// Path is a fresh, unguessable path in the container for the file.
func Path() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "/dev/shm/.dev-env-" + hex.EncodeToString(b)
}

// Render renders vars as shell assignments, for the stdin of WriteCommand.
// Keys must already be valid names.
func Render(vars []provider.EnvVar) []byte {
	var b strings.Builder
	for _, v := range vars {
		b.WriteString(v.Key + "=" + quote(v.Value) + "\n")
	}
	return []byte(b.String())
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WriteCommand stores its stdin at path, readable by its user alone. noclobber
// makes the create exclusive, so a path another user planted is never followed.
func WriteCommand(path string) []string {
	return []string{"sh", "-c", `umask 077; set -C; cat > "$1"`, "sh", path}
}

// Wrapper is the argv that exports the file's variables, deletes the file and
// only then execs command, so the values sit on disk until the command starts
// and in its environment after. The file is read whole before eval, so it is
// gone even when its content is not.
func Wrapper(path string, command []string) []string {
	const script = `f=$1; shift; c=$(cat "$f") && rm -f "$f" || exit 1; set -a; eval "$c"; set +a; exec "$@"`
	return append([]string{"sh", "-c", script, "sh", path}, command...)
}

// Pass writes vars into the container through write, which runs an argv there
// with the given stdin, and returns command wrapped to read them. With no vars
// it returns command untouched and writes nothing.
func Pass(vars []provider.EnvVar, command []string, write func(argv []string, stdin io.Reader) error) ([]string, error) {
	if len(vars) == 0 {
		return command, nil
	}
	for _, v := range vars {
		if err := xpath.ValidateEnvKey(v.Key); err != nil {
			return nil, err
		}
	}
	path := Path()
	if err := write(WriteCommand(path), bytes.NewReader(Render(vars))); err != nil {
		return nil, fmt.Errorf("passing the environment: %w", err)
	}
	return Wrapper(path, command), nil
}

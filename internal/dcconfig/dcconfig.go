// Package dcconfig locates a project's devcontainer configuration.
package dcconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoConfig means the folder ships no devcontainer configuration. In this
// tool that is a hard error, never a cue to generate one: the project owns its
// container definition.
var ErrNoConfig = errors.New("no devcontainer config")

// Find returns the devcontainer config file inside folder.
//
// Both spellings the devcontainer CLI accepts are honoured, in its own order of
// preference. The search deliberately does not walk up to parent directories:
// the folder is an explicit argument here, so looking above it would be
// guessing at which project the operator meant.
func Find(folder string) (string, error) {
	candidates := []string{
		filepath.Join(folder, ".devcontainer", "devcontainer.json"),
		filepath.Join(folder, ".devcontainer.json"),
	}
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w in %s", ErrNoConfig, folder)
}

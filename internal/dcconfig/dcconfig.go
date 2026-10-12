// Package dcconfig locates a project's devcontainer configuration.
package dcconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoConfig means the folder ships no devcontainer configuration. dev never
// writes into a project, so this package only reads.
var ErrNoConfig = errors.New("no devcontainer config")

// Find returns the devcontainer config inside folder, in the CLI's order of
// preference. It does not search parent directories.
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

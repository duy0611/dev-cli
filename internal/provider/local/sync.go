package local

import (
	"context"
	"fmt"
	"os"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// Sync does nothing: every command already passes the settings as --remote-env,
// so no copy in the container can drift. It says so on stderr rather than
// succeeding silently.
func (p *Provider) Sync(_ context.Context, _ model.Container, _ []provider.EnvVar) error {
	fmt.Fprintln(os.Stderr,
		"dev: a local container takes its settings on every command; nothing to push")
	return nil
}

package local

import (
	"context"
	"fmt"
	"os"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// Sync satisfies provider.Syncer by doing nothing, and that is honest here.
//
// The interface promises a state — after Sync, the container's copy of the
// workspace's settings matches the workspace — and a local container keeps no
// copy to diverge. Every command resolves the settings fresh and hands them to
// the container as --remote-env, so the process that reads them is decorated
// the moment it starts.
//
// What would close the remaining gap — a shell opened by some route other than
// dev — is a file of exported variables inside the container. That is refused:
// the settings hold tokens, these containers run programs that execute code on
// their own, and a file that outlives the command is exactly what the ssh agent
// relay exists to avoid. --remote-env is briefly visible in the host process
// list, a cost already accepted; a credential at rest in the container is not
// the same trade.
//
// It still says so rather than printing nothing, since the operator ran the
// command deliberately and an empty success reads as a command that failed
// quietly.
func (p *Provider) Sync(_ context.Context, _ model.Container, _ []provider.EnvVar) error {
	fmt.Fprintln(os.Stderr,
		"dev: a local container takes its settings on every command; nothing to push")
	return nil
}

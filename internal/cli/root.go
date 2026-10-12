// Package cli builds the `dev` command tree. Commands parse arguments and
// orchestrate; the work itself lives in the internal packages they call.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	// Blank imports register the providers with the factory.
	_ "github.com/duy0611/dev-cli/internal/provider/k8s"
	_ "github.com/duy0611/dev-cli/internal/provider/local"
)

// Execute runs the command tree and returns the process exit code. It does not
// os.Exit, so deferred cleanup still runs.
func Execute(version string) int {
	a := &app{out: os.Stdout, version: version}
	defer a.close()

	// Ctrl-C cancels the context rather than killing the process, so deferred
	// cleanup runs. The ssh agent relay needs this: killed outright, it leaves
	// its socket behind in a container.
	//
	// stop() restores default handling, so a second Ctrl-C still kills.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(version)
	root.AddCommand(
		newProviderCmd(a),
		newWorkspaceCmd(a),
		newContainerCmd(a),
		newWorktreeCmd(a),
		newAuditCmd(a),
	)

	// Cobra would print usage after every error, burying one-line failures.
	root.SilenceErrors = true
	root.SilenceUsage = true

	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(os.Stderr, "dev: %s\n", err)
	return exitCodeOf(err)
}

func newRootCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Manage devcontainers and the coding agents that run in them",
		Long: "dev manages devcontainers across providers and workspaces, and runs\n" +
			"coding agents inside them.",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		// Args is explicit: cobra's own unknown-command error is a plain error,
		// exit 1. noArgs makes it a usage error, exit 2.
		Args: noArgs(),
		RunE: groupRunE,
	}

	// Bad flags exit 2 like every other usage error, not 1 as cobra reports
	// them.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		c.SilenceUsage = false
		return usageError(err)
	})

	return cmd
}

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

	// Register the providers with the factory. Blank because nothing here
	// calls them directly; the registry is the whole interface.
	_ "github.com/duy0611/dev-cli/internal/provider/k8s"
	_ "github.com/duy0611/dev-cli/internal/provider/local"
)

// Execute runs the command tree and returns the process exit code.
//
// Returning the code rather than calling os.Exit keeps every deferred cleanup
// in the command path running, and leaves main() as the only place that exits.
func Execute(version string) int {
	a := &app{out: os.Stdout}
	defer a.close()

	// Ctrl-C cancels the command's context rather than killing the process, so
	// the deferred cleanup along the command path still runs. The ssh agent
	// relay is the case that needs it: killed outright it would leave its
	// socket in a container that outlives the session, and a socket nobody is
	// watching is a channel to the operator's agent nobody is watching.
	//
	// stop() restores the default handling, so a second Ctrl-C kills a command
	// that is ignoring the first.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(version)
	root.AddCommand(
		newProviderCmd(a),
		newWorkspaceCmd(a),
		newContainerCmd(a),
	)

	// Cobra prints usage after any error by default, which buries a one-line
	// failure under forty lines of flags. Errors are printed here instead, and
	// usage is shown only for the errors where it helps.
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
		// With no subcommand, print help rather than succeeding silently.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	// A bad flag is a malformed request, so it exits 2 like every other usage
	// error. Cobra reports these as plain errors, which would otherwise exit 1
	// and be indistinguishable from the work failing.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		c.SilenceUsage = false
		return usageError(err)
	})

	return cmd
}

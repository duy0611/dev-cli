package cli

import "github.com/spf13/cobra"

// Cobra reports a wrong argument count as a plain error, which exits 1. These
// wrappers make it a usage error, exit 2.

func exactArgs(n int) cobra.PositionalArgs {
	return wrapArgs(cobra.ExactArgs(n))
}

func noArgs() cobra.PositionalArgs {
	return wrapArgs(cobra.NoArgs)
}

func wrapArgs(inner cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := inner(cmd, args); err != nil {
			return usageError(err)
		}
		return nil
	}
}

// minArgs is for `exec`: cobra counts the arguments after -- too, so an exact
// count would reject the command being run.
func minArgs(n int) cobra.PositionalArgs {
	return wrapArgs(cobra.MinimumNArgs(n))
}

// groupRunE is the RunE for commands that only group subcommands. Without it
// cobra inherits the root's help-and-succeed RunE, so a mistyped subcommand
// exits 0.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		cmd.SilenceUsage = false
		return usageErrorf("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return cmd.Help()
}

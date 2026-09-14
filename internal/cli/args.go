package cli

import "github.com/spf13/cobra"

// Cobra reports a wrong argument count as a plain error, which would exit 1 and
// be indistinguishable from the work failing. These wrappers mark them as what
// they are: a malformed request, exit 2.

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

// minArgs is what `exec` needs: cobra hands the validator every positional
// argument, including the ones after --, so an exact count would reject the
// command being run.
func minArgs(n int) cobra.PositionalArgs {
	return wrapArgs(cobra.MinimumNArgs(n))
}

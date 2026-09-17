package cli

import (
	"io"

	"github.com/spf13/cobra"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
)

func newContainerToolsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List the tools a generated container can install",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerTools(a)
		},
	}
}

func runContainerTools(a *app) error {
	return a.table(func(w io.Writer) {
		header(w, "TOOL", "SUMMARY", "SOURCE")
		for _, t := range dcgen.Catalog() {
			// Whether a feature is published by the devcontainers project (or
			// the tool's own vendor) is the operator's business: the rest are
			// community images they are choosing to trust.
			source := "community"
			if t.Official {
				source = "official"
			}
			row(w, t.ID, t.Summary, source)
		}
	})
}

func newContainerConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect a generated container's configuration",
	}

	var workspace string
	show := &cobra.Command{
		Use:   "show NAME",
		Short: "Print the devcontainer config dev generated for a container",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerConfigShow(a, workspace, args[0])
		},
	}
	addWorkspaceFlag(show, &workspace)

	cmd.AddCommand(show)
	return cmd
}

func runContainerConfigShow(a *app, workspace, name string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if t.container.GeneratedConfig == "" {
		// Not an empty configuration: a different kind of container, whose
		// configuration is a file the operator can already open.
		return usageErrorf("container %s uses the project's own config: %s",
			name, t.container.ConfigPath)
	}
	a.printf("%s", t.container.GeneratedConfig)
	return nil
}

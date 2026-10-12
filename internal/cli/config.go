package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/duy0611/dev-cli/internal/dcgen"
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
		Short: "Inspect the configuration a container is built from",
		Args:  noArgs(),
		RunE:  groupRunE,
	}

	var workspace string
	show := &cobra.Command{
		Use:   "show NAME",
		Short: "Print the devcontainer config a container is built from",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerConfigShow(cmd.Context(), a, workspace, args[0])
		},
	}
	addWorkspaceFlag(show, &workspace)

	cmd.AddCommand(show)
	return cmd
}

func runContainerConfigShow(ctx context.Context, a *app, workspace, name string) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	if t.container.GeneratedConfig != "" {
		a.printf("%s", t.container.GeneratedConfig)
		return nil
	}

	// A project-owned container: the file on disk is not the whole answer,
	// since dev merges its state mount in per invocation. Printing only the
	// path would name a file that is not quite what ran.
	//
	// Fatal on a parse error, unlike stop or remove, since this must not show a
	// file dev could not use.
	if err := t.requireOverride(a); err != nil {
		return err
	}

	source := t.container.ConfigPath
	merged := t.container.OverrideConfigPath
	if merged == "" {
		// Without state there is no merge, so the project's file is what the
		// CLI gets.
		warnf(a, "%s (no merge: state off)", source)
		return printFile(a, source)
	}

	warnf(a, "merged from %s", source)
	return printFile(a, merged)
}

// printFile copies a configuration document to stdout. Warnings go to stderr,
// so `config show | jq` still gets valid JSON.
func printFile(a *app, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	a.printf("%s", body)
	return nil
}
